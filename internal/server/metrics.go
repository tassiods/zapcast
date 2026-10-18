// SPDX-FileCopyrightText: 2025 Tássio Dias <https://github.com/tassiods/zapcast>
// SPDX-License-Identifier: Apache-2.0

package server

// Runtime metrics via /debug/vars using expvar.

import (
	"expvar"
	"net/http"
	"runtime"
)

var (
	metricBroadcastsTotal      = expvar.NewInt("zapcast_broadcasts_total")
	metricBroadcastMessagesOK  = expvar.NewInt("zapcast_broadcast_messages_ok")
	metricBroadcastMessagesErr = expvar.NewInt("zapcast_broadcast_messages_err")
	metricBroadcastAborts      = expvar.NewInt("zapcast_broadcast_aborts")
	metricRateLimitBlocks      = expvar.NewMap("zapcast_rate_limit_blocks")
	metricHTTPRequests         = expvar.NewMap("zapcast_http_requests")
	metricSSEDrops             = expvar.NewInt("zapcast_sse_drops")
	metricSSESubscribers       = expvar.NewInt("zapcast_sse_subscribers")
	metricClientErrors         = expvar.NewMap("zapcast_client_errors")
	metricHTTPByRoute          = expvar.NewMap("zapcast_http_by_route")
	metricOutboxRetries        = expvar.NewInt("zapcast_outbox_retries")
	metricOutboxDelivered      = expvar.NewInt("zapcast_outbox_delivered")
)

// registerRuntimeMetrics publishes a goroutine count for leak detection.
func init() {
	expvar.Publish("zapcast_goroutines", expvar.Func(func() any {
		return runtime.NumGoroutine()
	}))
}

// handleMetrics is /debug/vars. Re-wired here so authMiddleware can gate it.
func handleMetrics(w http.ResponseWriter, r *http.Request) {
	expvar.Handler().ServeHTTP(w, r)
}
