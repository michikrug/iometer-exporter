package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/client_golang/prometheus/push"
)

type Reading struct {
	Typename string `json:"__typename"`
	Meter    Meter  `json:"meter"`
}

type Meter struct {
	Number  string       `json:"number"`
	Reading MeterReading `json:"reading"`
}

type MeterReading struct {
	Time      time.Time  `json:"time"`
	Registers []Register `json:"registers"`
}

type Register struct {
	Obis  string  `json:"obis"`
	Value float64 `json:"value"`
	Unit  string  `json:"unit"`
}

type Metric struct {
	gauge      prometheus.Gauge
	lastValue  float64
	lastUpdate time.Time
	expired    bool
}

type Status struct {
	Typename string `json:"__typename"`
	Meter    Meter  `json:"meter"`
	Device   Device `json:"device"`
}

type Device struct {
	Bridge Bridge `json:"bridge"`
	ID     string `json:"id"`
	Core   Core   `json:"core"`
}

type Bridge struct {
	RSSI    int    `json:"rssi"`
	Version string `json:"version"`
}

type Core struct {
	ConnectionStatus string `json:"connectionStatus"`
	RSSI             int    `json:"rssi"`
	Version          string `json:"version"`
	PowerStatus      string `json:"powerStatus"`
	BatteryLevel     int    `json:"batteryLevel"`
	AttachmentStatus string `json:"attachmentStatus"`
	PINStatus        string `json:"pinStatus"`
}

type Worker struct {
	client              *http.Client
	iometerHost         string
	deviceName          string
	metricsRegistry     *prometheus.Registry
	metricsCollector    map[string]*Metric
	collectingInterval  time.Duration
	expirationThreshold time.Duration
	pushGateway         PushGateway
}

type PushGateway struct {
	URL      string
	Username string
	Password string
}

var obisMap = map[string]string{
	"01-00:01.08.00*ff": "total_consumption",
	"01-00:02.08.00*ff": "total_production",
	"01-00:10.07.00*ff": "current_power",
}

func NewWorker(iometerHost, deviceName string, interval, expiration time.Duration, pushGateway PushGateway) *Worker {
	return &Worker{
		client:              &http.Client{},
		iometerHost:         iometerHost,
		deviceName:          deviceName,
		metricsRegistry:     prometheus.NewRegistry(),
		metricsCollector:    make(map[string]*Metric),
		collectingInterval:  interval,
		expirationThreshold: expiration,
		pushGateway:         pushGateway,
	}
}

func (w *Worker) fetchJSON(endpoint string, target interface{}) error {
	url := fmt.Sprintf("http://%s/%s", w.iometerHost, endpoint)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to fetch data, status code: %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(target)
}

func (w *Worker) fetchReadingData() (*Reading, error) {
	var data Reading
	if err := w.fetchJSON("v1/reading", &data); err != nil {
		return nil, err
	}
	return &data, nil
}

func (w *Worker) fetchStatusData() (*Status, error) {
	var data Status
	if err := w.fetchJSON("v1/status", &data); err != nil {
		return nil, err
	}
	return &data, nil
}

func (w *Worker) updateReadingMetrics() error {
	readingData, err := w.fetchReadingData()
	if err != nil {
		return err
	}
	for _, register := range readingData.Meter.Reading.Registers {
		if metricName, exists := obisMap[register.Obis]; exists {
			w.setMetric(metricName, register.Value)
		}
	}
	return nil
}

func (w *Worker) updateStatusMetrics() error {
	statusData, err := w.fetchStatusData()
	if err != nil {
		return err
	}
	w.setMetric("bridge_rssi", float64(statusData.Device.Bridge.RSSI))
	w.setMetric("core_rssi", float64(statusData.Device.Core.RSSI))
	w.setMetric("core_battery_level", float64(statusData.Device.Core.BatteryLevel))

	corePowerStatus := 0.0
	if statusData.Device.Core.PowerStatus == "battery" {
		corePowerStatus = 1.0
	}
	w.setMetric("core_power_status", corePowerStatus)

	coreConnectionStatus := 0.0
	if statusData.Device.Core.ConnectionStatus == "connected" {
		coreConnectionStatus = 1.0
	}
	w.setMetric("core_connection_status", coreConnectionStatus)

	coreAttachmentStatus := 0.0
	if statusData.Device.Core.AttachmentStatus == "attached" {
		coreAttachmentStatus = 1.0
	}
	w.setMetric("core_attachment_status", coreAttachmentStatus)

	corePINStatus := 0.0
	if statusData.Device.Core.PINStatus == "entered" {
		corePINStatus = 1.0
	}
	w.setMetric("core_pin_status", corePINStatus)

	return nil
}

func (w *Worker) updateMetrics() {
	updateOnce := func() {
		// Update reading metrics
		if err := w.updateReadingMetrics(); err != nil {
			log.Println("Error updating reading metrics:", err)
		}

		// Update status metrics
		if err := w.updateStatusMetrics(); err != nil {
			log.Println("Error updating status metrics:", err)
		}

		// Clear expired metrics
		w.clearExpiredMetrics()

		// Push metrics to PushGateway
		if w.pushGateway.URL != "" {
			w.pushMetrics()
		}
	}

	// Call the update immediately before starting the ticker.
	updateOnce()

	ticker := time.NewTicker(w.collectingInterval)
	defer ticker.Stop()

	for range ticker.C {
		updateOnce()
	}
}

func (w *Worker) pushMetrics() {
	pusher := push.New(w.pushGateway.URL, "iometer_exporter")
	if w.pushGateway.Username != "" && w.pushGateway.Password != "" {
		pusher = pusher.BasicAuth(w.pushGateway.Username, w.pushGateway.Password)
	}

	if err := pusher.Collector(w.metricsRegistry).Push(); err != nil {
		log.Printf("Failed to push metrics to PushGateway: %v", err)
	}
}

func (w *Worker) setMetric(key string, value float64) {
	if metric, exists := w.metricsCollector[key]; exists {
		// Only update the metric if the value has changed
		if metric.lastValue != value {
			if metric.expired {
				w.metricsRegistry.MustRegister(metric.gauge) // Re-register expired metric
				metric.expired = false
				log.Printf("Re-registered expired metric: %s", key)
			}
			metric.gauge.Set(value)
			metric.lastValue = value
			metric.lastUpdate = time.Now()
		}
	} else {
		gauge := prometheus.NewGauge(prometheus.GaugeOpts{
			Name:        fmt.Sprintf("iometer_%s", key),
			Help:        fmt.Sprintf("Metric from IOmeter API: %s.%s", w.deviceName, key),
			ConstLabels: prometheus.Labels{"device": w.deviceName},
		})
		w.metricsRegistry.MustRegister(gauge)
		gauge.Set(value)
		w.metricsCollector[key] = &Metric{gauge: gauge, lastValue: value, lastUpdate: time.Now(), expired: false}
		log.Printf("Registered new metric: %s", key)
	}
}

func (w *Worker) clearExpiredMetrics() {
	now := time.Now()
	for key, metric := range w.metricsCollector {
		if now.Sub(metric.lastUpdate) > w.expirationThreshold && !metric.expired {
			w.metricsRegistry.Unregister(metric.gauge) // Remove metric from Prometheus
			metric.expired = true
			metric.lastUpdate = now
			log.Printf("Marked metric as expired: %s", key)
		}
	}
}

// Check if all required environment variables are set
func checkEnvVars(vars []string) {
	for _, v := range vars {
		if os.Getenv(v) == "" {
			log.Fatalf("Missing required environment variable: %s", v)
		}
	}
}

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, relying on environment variables")
	}

	// Required environment variables
	requiredVars := []string{"IOMETER_HOST"}
	checkEnvVars(requiredVars)

	iometerHost := os.Getenv("IOMETER_HOST")
	deviceName := os.Getenv("DEVICE_NAME")
	if deviceName == "" {
		deviceName = iometerHost
	}

	collectingInterval := 1 * time.Minute
	if intervalStr, exists := os.LookupEnv("COLLECTING_INTERVAL"); exists {
		if interval, err := time.ParseDuration(intervalStr); err == nil && interval > 0 {
			collectingInterval = interval
		} else {
			log.Printf("Invalid COLLECTING_INTERVAL: %s (expected duration string, e.g., 30s, 1m, 2h): %v", intervalStr, err)
		}
	}

	expirationThreshold := 10 * time.Minute
	if thresholdStr, exists := os.LookupEnv("EXPIRATION_THRESHOLD"); exists {
		if threshold, err := time.ParseDuration(thresholdStr); err == nil && threshold > collectingInterval {
			expirationThreshold = threshold
		} else {
			log.Printf("Invalid EXPIRATION_THRESHOLD: %s (expected duration string, e.g., 30s, 1m, 2h): %v", thresholdStr, err)
		}
	}

	exporterPort := "9090"
	if port, exists := os.LookupEnv("EXPORTER_PORT"); exists {
		exporterPort = port
	}

	pushGateway := PushGateway{URL: os.Getenv("PUSHGATEWAY_URL"), Username: os.Getenv("PUSHGATEWAY_USERNAME"), Password: os.Getenv("PUSHGATEWAY_PASSWORD")}

	worker := NewWorker(iometerHost, deviceName, collectingInterval, expirationThreshold, pushGateway)
	go worker.updateMetrics()

	http.Handle("/metrics", promhttp.HandlerFor(worker.metricsRegistry, promhttp.HandlerOpts{}))
	server := &http.Server{Addr: ":" + exporterPort}

	go func() {
		log.Printf("Starting HTTP server on port %s", exporterPort)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server failed: %s", err)
		}
	}()

	// Graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	log.Println("Shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Fatalf("Server shutdown failed: %s", err)
	}
	log.Println("Server gracefully shut down")
}
