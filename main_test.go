package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCaptureWriterRecordsImplicitStatusOK(t *testing.T) {
	rr := httptest.NewRecorder()
	cw := &captureWriter{ResponseWriter: rr, tail: make([]byte, 0, responseTailBytes)}

	if _, err := cw.Write([]byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if cw.status != http.StatusOK {
		t.Fatalf("expected implicit status 200, got %d", cw.status)
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("recorder status = %d", rr.Code)
	}
}
