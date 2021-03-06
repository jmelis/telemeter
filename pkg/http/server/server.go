package server

import (
	"context"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/golang/snappy"
	clientmodel "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"

	"github.com/openshift/telemeter/pkg/metricfamily"
	"github.com/openshift/telemeter/pkg/store"
	"github.com/openshift/telemeter/pkg/store/instrumented"
)

// maxSampleAge is the maximum age of a sample that we can report via federation.
const maxSampleAge = 10 * time.Minute

type UploadValidator interface {
	ValidateUpload(ctx context.Context, req *http.Request) (string, metricfamily.Transformer, error)
}

type Server struct {
	receiveStore, store store.Store
	validator           UploadValidator
	nowFn               func() time.Time
}

func New(store store.Store, validator UploadValidator) *Server {
	return &Server{
		receiveStore: instrumented.New(nil, "received"),
		store:        store,
		validator:    validator,
		nowFn:        time.Now,
	}
}

func NewNonExpiring(store store.Store, validator UploadValidator) *Server {
	return &Server{
		store:     store,
		validator: validator,
		nowFn:     nil,
	}
}

func (s *Server) Get(w http.ResponseWriter, req *http.Request) {
	if req.Method != "GET" {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	format := expfmt.Negotiate(req.Header)
	encoder := expfmt.NewEncoder(w, format)
	ctx := context.Background()

	// samples older than 10 minutes must be ignored
	var minTimeMs int64
	var filter metricfamily.MultiTransformer
	if s.nowFn != nil {
		minTime := s.nowFn().Add(-maxSampleAge)
		minTimeMs = minTime.UnixNano() / int64(time.Millisecond)
		filter.With(metricfamily.NewDropExpiredSamples(minTime))
		filter.With(metricfamily.TransformerFunc(metricfamily.PackMetrics))
	}

	filter.With(metricfamily.TransformerFunc(metricfamily.DropTimestamp))

	err := s.store.ReadMetrics(ctx, minTimeMs, func(partitionKey string, families []*clientmodel.MetricFamily) error {
		for _, family := range families {
			if family == nil {
				continue
			}
			if ok, err := filter.Transform(family); err != nil || !ok {
				continue
			}
			if err := encoder.Encode(family); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func (s *Server) Post(w http.ResponseWriter, req *http.Request) {
	if req.Method != "POST" {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	defer req.Body.Close()

	ctx, cancel := context.WithTimeout(req.Context(), 5*time.Second)
	defer cancel()

	partitionKey, transforms, err := s.validator.ValidateUpload(ctx, req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// read the response into memory
	format := expfmt.ResponseFormat(req.Header)
	var r io.Reader = req.Body
	if req.Header.Get("Content-Encoding") == "snappy" {
		r = snappy.NewReader(r)
	}
	decoder := expfmt.NewDecoder(r, format)

	errCh := make(chan error)
	go func() { errCh <- s.decodeAndStoreMetrics(ctx, partitionKey, decoder, transforms) }()

	select {
	case <-ctx.Done():
		http.Error(w, "Timeout while storing metrics", http.StatusInternalServerError)
		log.Printf("timeout processing incoming request")
		return
	case err := <-errCh:
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
}

func (s *Server) decodeAndStoreMetrics(ctx context.Context, partitionKey string, decoder expfmt.Decoder, transformer metricfamily.Transformer) error {
	families := make([]*clientmodel.MetricFamily, 0, 100)
	for {
		family := &clientmodel.MetricFamily{}
		families = append(families, family)
		if err := decoder.Decode(family); err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
	}

	if err := s.receiveStore.WriteMetrics(ctx, partitionKey, families); err != nil {
		return err
	}

	// filter the list
	for i, family := range families {
		ok, err := transformer.Transform(family)
		if err != nil {
			return err
		}
		if !ok {
			families[i] = nil
			continue
		}
	}

	families = metricfamily.Pack(families)

	return s.store.WriteMetrics(ctx, partitionKey, families)
}
