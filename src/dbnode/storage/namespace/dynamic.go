// Copyright (c) 2017 Uber Technologies, Inc.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package namespace

import (
	"errors"
	"sync"
	"time"

	nsproto "github.com/m3db/m3/src/dbnode/generated/proto/namespace"
	"github.com/m3db/m3cluster/kv"
	xlog "github.com/m3db/m3x/log"
	xwatch "github.com/m3db/m3x/watch"

	"github.com/uber-go/tally"
)

var (
	errInitTimeOut           = errors.New("timed out waiting for initial value")
	errRegistryAlreadyClosed = errors.New("registry already closed")
	errInvalidRegistry       = errors.New("could not parse latest value from config service")
)

type dynamicInitializer struct {
	sync.Mutex
	opts DynamicOptions
	reg  Registry
}

// NewDynamicInitializer returns a dynamic namespace initializer
func NewDynamicInitializer(opts DynamicOptions) Initializer {
	return &dynamicInitializer{opts: opts}
}

func (i *dynamicInitializer) Init() (Registry, error) {
	i.Lock()
	defer i.Unlock()

	if i.reg != nil {
		return i.reg, nil
	}

	if err := i.opts.Validate(); err != nil {
		return nil, err
	}

	reg, err := newDynamicRegistry(i.opts)
	if err != nil {
		return nil, err
	}

	i.reg = reg
	return i.reg, nil
}

type dynamicRegistry struct {
	sync.RWMutex
	opts         DynamicOptions
	logger       xlog.Logger
	metrics      dynamicRegistryMetrics
	watchable    xwatch.Watchable
	kvWatch      kv.ValueWatch
	currentValue kv.Value
	currentMap   Map
	closed       bool
}

type dynamicRegistryMetrics struct {
	numInvalidUpdates tally.Counter
	currentVersion    tally.Gauge
}

func newDynamicRegistryMetrics(opts DynamicOptions) dynamicRegistryMetrics {
	scope := opts.InstrumentOptions().MetricsScope().SubScope("namespace-registry")
	return dynamicRegistryMetrics{
		numInvalidUpdates: scope.Counter("invalid-update"),
		currentVersion:    scope.Gauge("current-version"),
	}
}

func newDynamicRegistry(opts DynamicOptions) (Registry, error) {
	kvStore, err := opts.ConfigServiceClient().KV()
	if err != nil {
		return nil, err
	}

	watch, err := kvStore.Watch(opts.NamespaceRegistryKey())
	if err != nil {
		return nil, err
	}

	logger := opts.InstrumentOptions().Logger()
	if err = waitOnInit(watch, opts.InitTimeout()); err != nil {
		logger.Errorf("dynamic namespace registry initialization timed out in %s: %v",
			opts.InitTimeout().String(), err)
		return nil, err
	}

	initValue := watch.Get()
	m, err := getMapFromUpdate(initValue)
	if err != nil {
		logger.Errorf("dynamic namespace registry received invalid initial value: %v",
			err)
		return nil, err
	}

	watchable := xwatch.NewWatchable()
	watchable.Update(m)

	dt := &dynamicRegistry{
		opts:         opts,
		logger:       logger,
		metrics:      newDynamicRegistryMetrics(opts),
		watchable:    watchable,
		kvWatch:      watch,
		currentValue: initValue,
		currentMap:   m,
	}
	go dt.run()
	go dt.reportMetrics()
	return dt, nil
}

func (r *dynamicRegistry) isClosed() bool {
	r.RLock()
	closed := r.closed
	r.RUnlock()
	return closed
}

func (r *dynamicRegistry) value() kv.Value {
	r.RLock()
	defer r.RUnlock()
	return r.currentValue
}

func (r *dynamicRegistry) maps() Map {
	r.RLock()
	defer r.RUnlock()
	return r.currentMap
}

func (r *dynamicRegistry) reportMetrics() {
	ticker := time.NewTicker(r.opts.InstrumentOptions().ReportInterval())
	defer ticker.Stop()

	for range ticker.C {
		if r.isClosed() {
			return
		}

		r.metrics.currentVersion.Update(float64(r.value().Version()))
	}
}

func (r *dynamicRegistry) run() {
	for !r.isClosed() {
		if _, ok := <-r.kvWatch.C(); !ok {
			r.Close()
			break
		}

		val := r.kvWatch.Get()
		if val == nil {
			r.metrics.numInvalidUpdates.Inc(1)
			r.logger.Warnf("dynamic namespace registry received nil, skipping")
			continue
		}

		if !val.IsNewer(r.currentValue) {
			r.metrics.numInvalidUpdates.Inc(1)
			r.logger.Warnf("dynamic namespace registry received older version: %v, skipping",
				val.Version())
			continue
		}

		m, err := getMapFromUpdate(val)
		if err != nil {
			r.metrics.numInvalidUpdates.Inc(1)
			r.logger.Warnf("dynamic namespace registry received invalid update: %v, skipping",
				err)
			continue
		}

		if m.Equal(r.maps()) {
			r.metrics.numInvalidUpdates.Inc(1)
			r.logger.Warnf("dynamic namespace registry received identical update, skipping")
			continue
		}

		r.logger.Infof("dynamic namespace registry updated to version: %d", val.Version())
		r.Lock()
		r.currentValue = val
		r.currentMap = m
		r.watchable.Update(m)
		r.Unlock()
	}
}

func (r *dynamicRegistry) Watch() (Watch, error) {
	_, w, err := r.watchable.Watch()
	if err != nil {
		return nil, err
	}
	return NewWatch(w), err
}

func (r *dynamicRegistry) Close() error {
	r.Lock()
	defer r.Unlock()

	if r.closed {
		return errRegistryAlreadyClosed
	}

	r.closed = true

	r.kvWatch.Close()
	r.watchable.Close()
	return nil
}

func waitOnInit(w kv.ValueWatch, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	select {
	case <-w.C():
		return nil
	case <-time.After(d):
		return errInitTimeOut
	}
}

func getMapFromUpdate(val kv.Value) (Map, error) {
	if val == nil {
		return nil, errInvalidRegistry
	}

	var protoRegistry nsproto.Registry
	if err := val.Unmarshal(&protoRegistry); err != nil {
		return nil, errInvalidRegistry
	}

	return FromProto(protoRegistry)
}
