package server

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestIntegrationDeliveryAttemptMetricsRemainPublished(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure error
	}{{"acknowledged", nil}, {"uncertain", errors.New("synthetic uncertain network error")}} {
		t.Run(tc.name, func(t *testing.T) {
			app := newTestAppWithWA(t, &fakeWA{connected: true, sendErrAll: tc.failure})
			id, err := app.contacts.CreateBroadcast(ctx, "synthetic", "hi", 1, 0, 0, "", "")
			if err != nil {
				t.Fatal(err)
			}
			beforeOK, beforeErr := metricBroadcastMessagesOK.Value(), metricBroadcastMessagesErr.Value()
			ch := runBroadcastForTest(t, app, ctx, id, "synthetic", "hi", []Contact{{Name: "Synthetic", Phone: "5511111111111"}}, nil, nil, "", "", "")
			defer app.unsubscribe(ch)
			drainUntilDone(t, ch, 2*time.Second)
			app.broadcaster.wg.Wait()
			if tc.failure == nil && metricBroadcastMessagesOK.Value() != beforeOK+1 {
				t.Fatal("acknowledged provider attempts missing from metrics")
			}
			if tc.failure != nil && metricBroadcastMessagesErr.Value() != beforeErr+1 {
				t.Fatal("uncertain provider attempts missing from metrics")
			}
		})
	}
}
func TestIntegrationBroadcastAbortMetricRemainsPublished(t *testing.T) {
	app := newTestAppWithWA(t, &fakeWA{connected: true})
	id, err := app.contacts.CreateBroadcast(ctx, "synthetic", "hi", 1, 0, 0, "", "")
	if err != nil {
		t.Fatal(err)
	}
	c, cancel := context.WithCancel(ctx)
	cancel()
	before := metricBroadcastAborts.Value()
	ch := runBroadcastForTest(t, app, c, id, "synthetic", "hi", []Contact{{Name: "Synthetic", Phone: "5511111111111"}}, nil, nil, "", "", "")
	defer app.unsubscribe(ch)
	drainUntilDone(t, ch, 2*time.Second)
	app.broadcaster.wg.Wait()
	if metricBroadcastAborts.Value() != before+1 {
		t.Fatal("aborted broadcasts missing from metrics")
	}
}
