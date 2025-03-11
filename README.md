# IOmeter Exporter

IOmeter Exporter is a Prometheus exporter for IOmeter devices. It fetches device status from the IOmeter API and exposes relevant metrics for monitoring and analysis.

## Features

- Fetches real-time data from IOmeter devices
- Exposes Prometheus-compatible metrics
- Configurable via environment variables
- Automatic expiration of stale metrics
- Support for optionally using a Pushgateway

## Requirements

- Go 1.18+
- IOmeter device with local API
- Prometheus for scraping metrics

## Installation

```sh
git clone https://github.com/michikrug/iometer-exporter.git
cd iometer-exporter
go build -o iometer-exporter
```

### For usage on ARM devices like RaspberryPI

```sh
GOOS=linux GOARCH=arm GOARM=7 go build -o iometer-exporter
```

## Configuration

Create a `.env` file or use environment variables:

```ini
DEVICE_NAME=IOmeter
IOMETER_HOST=192.168.12.34
COLLECTING_INTERVAL=1m
EXPIRATION_THRESHOLD=10m
EXPORTER_PORT=9090
PUSHGATEWAY_URL=optional-pushgateway-url
PUSHGATEWAY_USERNAME=optional-username
PUSHGATEWAY_PASSWORD=optional-password
```

### Running the Exporter

```sh
./iometer-exporter
```

### Running with Docker

```sh
docker build -t iometer-exporter .
docker run --env-file .env -p 9090:9090 iometer-exporter
```

## Metrics

The exporter exposes metrics at:

```sh
http://localhost:9090/metrics
```

Example metrics:

```sh
# HELP iometer_current_power Metric from IOmeter API: IOMeter.current_power
# TYPE iometer_current_power gauge
iometer_current_power{device="IOMeter"} 129
```

## License

This project is licensed under the GNU General Public License v3. See the [LICENSE](LICENSE) file for details.

## Contributions

Contributions are welcome! Feel free to open an issue or submit a pull request.

## Contact

For questions, open an issue on [GitHub](https://github.com/michikrug/iometer-exporter).
