// Package webfixture includes a simple HTTP-server test fixture that collects
// the requests made to it
package webfixture

import (
	"io"
	"net/http"
	"net/http/httptest"
)

// RequestTrace represents simple data about a web request
type RequestTrace struct {
	Method, Path, Body string
}

// TraceRequestsFrom runs a small web server, and then invokes a provided test
// function while passing it the server URL and an HTTP client. The requests
// made to the web server while the test function is running are then logged
// and returned.
func TraceRequestsFrom(test_func func(url string, c *http.Client)) (requests []RequestTrace) {
	requestsChan := make(chan RequestTrace)
	svr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			panic(err)
		}
		requestsChan <- RequestTrace{r.Method, r.URL.Path, string(body)}
	}))
	done := make(chan struct{})
	go func() {
		defer close(done)
		for request := range requestsChan {
			requests = append(requests, request)
		}
	}()
	// Shutdown order: stop the server before closing the channel so handlers
	// cannot send on a closed channel. Runs on normal return, panic, or
	// runtime.Goexit from test_func.
	defer func() {
		svr.Close()
		svr.CloseClientConnections()
		close(requestsChan)
		<-done
	}()
	test_func(svr.URL, svr.Client())
	return
}
