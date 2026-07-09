package api

import (
	"net/http"
	"testing"
)

// TestCatalogHasNo422 enforces the Issue-5577 invariant: no error code maps to
// HTTP 422. 422 mislabels server-side state (missing/corrupted/empty repo) as a
// client error; those are 404 / 500 / 200 respectively.
func TestCatalogHasNo422(t *testing.T) {
	for code, e := range errorCatalog {
		if e.Status == http.StatusUnprocessableEntity {
			t.Errorf("code %s maps to 422 — forbidden (use 404/500/200 by cause)", code)
		}
	}
}

// TestCatalogCoversAllCodes ensures every defined ErrorCodeEnum has a catalog
// entry with a valid status, so NewCoded never silently degrades a real code.
func TestCatalogCoversAllCodes(t *testing.T) {
	for _, code := range AllErrorCodes {
		e, ok := errorCatalog[code]
		if !ok {
			t.Errorf("code %s missing from errorCatalog", code)
			continue
		}
		if e.Status < 200 || e.Status > 599 {
			t.Errorf("code %s has invalid status %d", code, e.Status)
		}
	}
}

// TestNewCoded derives status + code from the catalog and honours an override
// message.
func TestNewCoded(t *testing.T) {
	he := NewCoded(ErrCodeRepositoryNotFound)
	if he.Status != http.StatusNotFound {
		t.Errorf("status = %d, want 404", he.Status)
	}
	if he.Code != "REPOSITORY_NOT_FOUND" {
		t.Errorf("code = %q, want REPOSITORY_NOT_FOUND", he.Code)
	}
	if he.Message != "repository not found" {
		t.Errorf("message = %q", he.Message)
	}

	custom := NewCoded(ErrCodeRepositoryCorrupted, "branch listing failed")
	if custom.Status != http.StatusInternalServerError {
		t.Errorf("override status = %d, want 500", custom.Status)
	}
	if custom.Message != "branch listing failed" {
		t.Errorf("override message = %q", custom.Message)
	}
}
