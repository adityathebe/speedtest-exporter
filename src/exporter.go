package main

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/showwin/speedtest-go/speedtest"
)

const (
	namespace = "speedtest"
)

var (
	up = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "up"),
		"Was the last speedtest successful.",
		[]string{"test_uuid"}, nil,
	)
	scrapeDurationSeconds = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "scrape_duration_seconds"),
		"Time to preform last speed test",
		[]string{"test_uuid"}, nil,
	)
	latency = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "latency_milliseconds"),
		"Measured latency on last speed test in milliseconds",
		[]string{"test_uuid", "user_lat", "user_lon", "user_ip", "user_isp", "server_lat", "server_lon", "server_id", "server_name", "server_country", "distance"},
		nil,
	)
	jitter = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "jitter_milliseconds"),
		"Measured jitter on last speed test in milliseconds",
		[]string{"test_uuid", "user_lat", "user_lon", "user_ip", "user_isp", "server_lat", "server_lon", "server_id", "server_name", "server_country", "distance"},
		nil,
	)
	upload = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "upload_speed_bytes_per_second"),
		"Last upload speedtest result",
		[]string{"test_uuid", "user_lat", "user_lon", "user_ip", "user_isp", "server_lat", "server_lon", "server_id", "server_name", "server_country", "distance"},
		nil,
	)
	download = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "", "download_speed_bytes_per_second"),
		"Last download speedtest result",
		[]string{"test_uuid", "user_lat", "user_lon", "user_ip", "user_isp", "server_lat", "server_lon", "server_id", "server_name", "server_country", "distance"},
		nil,
	)
)

// cachedMetrics holds the cached speedtest results
type cachedMetrics struct {
	testUUID     string
	duration     float64
	success      bool
	latencyData  *prometheus.Metric
	jitterData   *prometheus.Metric
	uploadData   *prometheus.Metric
	downloadData *prometheus.Metric
}

// Exporter runs speedtest and exports them using
// the prometheus metrics package.
type Exporter struct {
	serverID       int
	serverFallback bool
	cache          cachedMetrics
	mu             sync.RWMutex
	ready          atomic.Bool
}

// NewExporter returns an initialized Exporter.
func NewExporter(serverID int, serverFallback bool) (*Exporter, error) {
	return &Exporter{
		serverID:       serverID,
		serverFallback: serverFallback,
	}, nil
}

// Describe describes all the metrics. It implements prometheus.Collector.
func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	ch <- up
	ch <- scrapeDurationSeconds
	ch <- latency
	ch <- jitter
	ch <- upload
	ch <- download
}

// Collect returns the cached metrics. It implements prometheus.Collector.
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if !e.ready.Load() {
		// Return no metrics if not ready yet
		return
	}

	// Return cached metrics
	if e.cache.success {
		ch <- prometheus.MustNewConstMetric(
			up, prometheus.GaugeValue, 1.0,
			e.cache.testUUID,
		)
		ch <- prometheus.MustNewConstMetric(
			scrapeDurationSeconds, prometheus.GaugeValue, e.cache.duration,
			e.cache.testUUID,
		)
		if e.cache.latencyData != nil {
			ch <- *e.cache.latencyData
		}
		if e.cache.jitterData != nil {
			ch <- *e.cache.jitterData
		}
		if e.cache.uploadData != nil {
			ch <- *e.cache.uploadData
		}
		if e.cache.downloadData != nil {
			ch <- *e.cache.downloadData
		}
	} else {
		ch <- prometheus.MustNewConstMetric(
			up, prometheus.GaugeValue, 0.0,
			e.cache.testUUID,
		)
	}
}

// Refresh runs a speedtest and updates the cached metrics
func (e *Exporter) Refresh(ctx context.Context) error {
	testUUID := uuid.New().String()
	start := time.Now()

	var latencyMetric, jitterMetric, uploadMetric, downloadMetric *prometheus.Metric
	if err := e.runSpeedtest(ctx, testUUID, &latencyMetric, &jitterMetric, &uploadMetric, &downloadMetric); err != nil {
		return err
	}

	cache := cachedMetrics{
		testUUID:     testUUID,
		duration:     time.Since(start).Seconds(),
		success:      true,
		latencyData:  latencyMetric,
		jitterData:   jitterMetric,
		uploadData:   uploadMetric,
		downloadData: downloadMetric,
	}

	e.mu.Lock()
	e.cache = cache
	e.mu.Unlock()
	e.ready.Store(true)

	return nil
}

// Ready returns whether the exporter has completed at least one speedtest
func (e *Exporter) Ready() bool {
	return e.ready.Load()
}

func (e *Exporter) runSpeedtest(ctx context.Context, testUUID string, latencyMetric, jitterMetric, uploadMetric, downloadMetric **prometheus.Metric) error {
	client := speedtest.New()

	logger.Debug("Fetching user information")
	user, err := client.FetchUserInfoContext(ctx)
	if err != nil {
		return fmt.Errorf("could not fetch user information: %w", err)
	}

	logger.Debug("Fetching server list")
	serverList, err := client.FetchServerListContext(ctx)
	if err != nil {
		return fmt.Errorf("could not fetch server list: %w", err)
	}
	logger.Debug("Server list fetched", "count", len(serverList))

	var server *speedtest.Server
	if e.serverID == -1 {
		if len(serverList) == 0 {
			return fmt.Errorf("no servers available")
		}

		server = serverList[0]
		logger.Debug("Selected closest server",
			"server_id", server.ID,
			"server_name", server.Name,
			"server_country", server.Country,
			"distance", server.Distance,
		)
	} else {
		servers, err := serverList.FindServer([]int{e.serverID})
		if err != nil {
			return fmt.Errorf("error finding server: %w", err)
		}

		if len(servers) == 0 || servers[0].ID != fmt.Sprintf("%d", e.serverID) {
			if !e.serverFallback {
				return fmt.Errorf("could not find chosen server ID %d in the list of available servers, server_fallback is not set", e.serverID)
			}
			logger.Debug("Configured server not found, falling back to closest",
				"requested_server_id", e.serverID,
			)
			if len(serverList) == 0 {
				return fmt.Errorf("no servers available for fallback")
			}
			server = serverList[0]
			logger.Debug("Selected fallback server",
				"server_id", server.ID,
				"server_name", server.Name,
				"server_country", server.Country,
			)
		} else {
			server = servers[0]
			logger.Debug("Selected configured server",
				"server_id", server.ID,
				"server_name", server.Name,
				"server_country", server.Country,
			)
		}
	}

	logger.Debug("Running speedtest (ping, download, upload)")
	if err := server.TestAll(); err != nil {
		return fmt.Errorf("speedtest failed: %w", err)
	}

	logger.Debug("Speedtest completed",
		"latency_ms", server.Latency.Milliseconds(),
		"jitter_ms", server.Jitter.Milliseconds(),
		"download_bps", server.DLSpeed,
		"upload_bps", server.ULSpeed,
	)

	// Create metrics
	latencyMetricValue := prometheus.MustNewConstMetric(
		latency, prometheus.GaugeValue, float64(server.Latency.Milliseconds()),
		testUUID,
		user.Lat,
		user.Lon,
		user.IP,
		user.Isp,
		server.Lat,
		server.Lon,
		server.ID,
		server.Name,
		server.Country,
		fmt.Sprintf("%f", server.Distance),
	)
	*latencyMetric = &latencyMetricValue

	jitterMetricValue := prometheus.MustNewConstMetric(
		jitter, prometheus.GaugeValue, float64(server.Jitter.Milliseconds()),
		testUUID,
		user.Lat,
		user.Lon,
		user.IP,
		user.Isp,
		server.Lat,
		server.Lon,
		server.ID,
		server.Name,
		server.Country,
		fmt.Sprintf("%f", server.Distance),
	)
	*jitterMetric = &jitterMetricValue

	downloadMetricValue := prometheus.MustNewConstMetric(
		download, prometheus.GaugeValue, float64(server.DLSpeed),
		testUUID,
		user.Lat,
		user.Lon,
		user.IP,
		user.Isp,
		server.Lat,
		server.Lon,
		server.ID,
		server.Name,
		server.Country,
		fmt.Sprintf("%f", server.Distance),
	)
	*downloadMetric = &downloadMetricValue

	uploadMetricValue := prometheus.MustNewConstMetric(
		upload, prometheus.GaugeValue, float64(server.ULSpeed),
		testUUID,
		user.Lat,
		user.Lon,
		user.IP,
		user.Isp,
		server.Lat,
		server.Lon,
		server.ID,
		server.Name,
		server.Country,
		fmt.Sprintf("%f", server.Distance),
	)
	*uploadMetric = &uploadMetricValue

	return nil
}
