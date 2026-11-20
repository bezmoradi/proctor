package applogs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	OTelReceiver   = "proctor-sim-otel"
	otelStatsPath  = "/__proctor/stats"
	otelLogsPath   = "/__proctor/logs"
	otelMaxAnswer  = 64 << 20
	otelReadLimit  = 10 * time.Second
	otelAttributes = "attributes"
)

var otelReserved = map[string]bool{"time": true, "level": true, "msg": true, "service": true, "trace_id": true, "span_id": true, otelAttributes: true}

type OTel struct {
	Endpoint string
}

type otelStats struct {
	Receiver   string `json:"receiver"`
	NextLogSeq uint64 `json:"next_log_seq"`
	Refused    struct {
		Logs uint64 `json:"logs"`
	} `json:"refused"`
}

type otelRecord struct {
	Time         string         `json:"time"`
	ObservedTime string         `json:"observed_time"`
	Severity     string         `json:"severity"`
	Body         any            `json:"body"`
	Service      string         `json:"service"`
	TraceID      string         `json:"trace_id"`
	SpanID       string         `json:"span_id"`
	Attributes   map[string]any `json:"attributes"`
}

type otelAnswer struct {
	Matched *int         `json:"matched"`
	Records []otelRecord `json:"records"`
	Stats   *otelStats   `json:"stats"`
}

func CheckOTelEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	switch {
	case err != nil:
		return err
	case u.Scheme != "http" && u.Scheme != "https":
		return errors.New("must be an http or https URL, such as http://127.0.0.1:4318")
	case u.Host == "":
		return errors.New("has no host")
	case u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery:
		return errors.New("can't contain credentials, a query or a fragment")
	}
	return nil
}

func (o OTel) url(p string) string {
	return strings.TrimSuffix(o.Endpoint, "/") + p
}

func (o OTel) do(method, p string, body []byte, out any) error {
	ctx, cancel := context.WithTimeout(context.Background(), otelReadLimit)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, o.url(p), bytes.NewReader(body))
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, otelMaxAnswer+1))
	if err != nil {
		return err
	}
	if len(data) > otelMaxAnswer {
		return fmt.Errorf("%s %s answered more than %d bytes", method, p, otelMaxAnswer)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s %s answered HTTP %d; is %s a %s receiver?", method, p, resp.StatusCode, o.Endpoint, OTelReceiver)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("%s %s answered something that isn't the receiver's JSON: %v", method, p, err)
	}
	return nil
}

func (o OTel) stats() (otelStats, error) {
	var st otelStats
	if err := o.do(http.MethodGet, otelStatsPath, nil, &st); err != nil {
		return st, err
	}
	if st.Receiver != OTelReceiver || st.NextLogSeq == 0 {
		return st, fmt.Errorf("%s isn't a %s receiver (GET %s answered receiver %q)", o.Endpoint, OTelReceiver, otelStatsPath, st.Receiver)
	}
	return st, nil
}

func (o OTel) Mark() (Mark, error) {
	st, err := o.stats()
	if err != nil {
		return Mark{}, err
	}
	return Mark{seq: st.NextLogSeq, refused: st.Refused.Logs}, nil
}

func (o OTel) Since(m Mark) (Window, error) {
	if m.seq == 0 {
		return Window{}, errors.New("the scenario has no start position in the receiver's logs")
	}
	body, _ := json.Marshal(map[string]any{"after": m.seq - 1, "all": true})
	var ans otelAnswer
	if err := o.do(http.MethodPost, otelLogsPath, body, &ans); err != nil {
		return Window{}, err
	}
	switch {
	case ans.Stats == nil || ans.Matched == nil:
		return Window{}, fmt.Errorf("POST %s answered without matched and stats; is %s a %s receiver?", otelLogsPath, o.Endpoint, OTelReceiver)
	case ans.Stats.NextLogSeq < m.seq:
		return Window{}, fmt.Errorf("the receiver's next log sequence number went from %d back to %d while the scenario ran, so it was restarted and log records may be missing", m.seq, ans.Stats.NextLogSeq)
	case ans.Stats.Refused.Logs > m.refused:
		return Window{}, fmt.Errorf("the receiver refused %d log record(s) while the scenario ran (it was full, or a record was too large), and a refused record could be an error", ans.Stats.Refused.Logs-m.refused)
	case *ans.Matched != len(ans.Records):
		return Window{}, fmt.Errorf("the receiver matched %d log records but returned %d", *ans.Matched, len(ans.Records))
	}
	var w Window
	for _, rec := range ans.Records {
		line, err := otelLine(rec)
		if err != nil {
			return Window{}, err
		}
		w.Lines = append(w.Lines, line)
	}
	return w, nil
}

func otelLine(rec otelRecord) (string, error) {
	var b bytes.Buffer
	first := true
	put := func(k string, v any) error {
		if !first {
			b.WriteByte(',')
		}
		first = false
		b.Write(encodeKey(k))
		b.WriteByte(':')
		var vb bytes.Buffer
		enc := json.NewEncoder(&vb)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(v); err != nil {
			return err
		}
		b.Write(bytes.TrimSuffix(vb.Bytes(), []byte("\n")))
		return nil
	}
	at := rec.Time
	if at == "" {
		at = rec.ObservedTime
	}
	b.WriteByte('{')
	fields := []struct {
		k string
		v any
	}{{"time", at}, {"level", rec.Severity}, {"msg", text(rec.Body)}, {"service", rec.Service}, {"trace_id", rec.TraceID}, {"span_id", rec.SpanID}}
	for _, f := range fields {
		if f.k == "level" && rec.Severity == "" {
			continue
		}
		if err := put(f.k, f.v); err != nil {
			return "", err
		}
	}
	keys := make([]string, 0, len(rec.Attributes))
	for k := range rec.Attributes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	clashing := map[string]any{}
	for _, k := range keys {
		if otelReserved[k] {
			clashing[k] = rec.Attributes[k]
			continue
		}
		if err := put(k, rec.Attributes[k]); err != nil {
			return "", err
		}
	}
	if len(clashing) > 0 {
		if err := put(otelAttributes, clashing); err != nil {
			return "", err
		}
	}
	b.WriteByte('}')
	return b.String(), nil
}
