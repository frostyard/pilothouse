package files

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/frostyard/pilothouse/internal/auth"
	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/capability"
	"github.com/frostyard/pilothouse/internal/platform"
	"github.com/stretchr/testify/assert"
)

type filesHost struct {
	admin            bool
	state            State
	queryID          string
	parameters       map[string]string
	queryErr         error
	queryCalls       int
	stream           broker.StreamResult
	streamErr        error
	streamID         string
	streamCalls      int
	streamActionID   string
	streamParameters map[string]string
	streamBody       bytes.Buffer
	streamActionErr  error
	streamAction     func(context.Context, *http.Request, string, map[string]string, io.Reader) error
	discardStream    bool
	streamSize       int64
	csrf             string
	csrfCalls        int
	validCSRF        bool
	page             platform.Page
}

func (*filesHost) Capabilities(context.Context) capability.Set {
	return capability.New(capability.Systemd, capability.Journald, capability.Updex, capability.Sysext, capability.Bootc, capability.RPMOStree, capability.AutoupdateRPMOStree, capability.AutoupdateBootc, capability.Podman, capability.Docker, capability.Incus)
}
func (*filesHost) ConfirmAction(http.ResponseWriter, *http.Request, string, string) bool { return true }
func (h *filesHost) CSRFToken(*http.Request) string                                      { return h.csrf }
func (*filesHost) Execute(context.Context, *http.Request, string, map[string]string) error {
	return nil
}
func (h *filesHost) Identity(*http.Request) auth.Identity { return auth.Identity{Admin: h.admin} }
func (h *filesHost) Query(_ context.Context, id string, parameters map[string]string, target any) error {
	h.queryCalls++
	h.queryID, h.parameters = id, parameters
	if h.queryErr != nil {
		return h.queryErr
	}
	*(target.(*State)) = h.state
	return nil
}
func (h *filesHost) Render(w http.ResponseWriter, _ *http.Request, page platform.Page) error {
	h.page = page
	return page.Body.Render(context.Background(), w)
}
func (*filesHost) ValidateAction(http.ResponseWriter, *http.Request) bool { return true }
func (h *filesHost) ValidateActionToken(_ http.ResponseWriter, _ *http.Request, token string) bool {
	h.csrfCalls++
	return h.validCSRF && token == h.csrf
}
func (h *filesHost) StreamAction(ctx context.Context, request *http.Request, id string, parameters map[string]string, body io.Reader) error {
	h.streamCalls++
	h.streamActionID, h.streamParameters = id, parameters
	if h.streamAction != nil {
		return h.streamAction(ctx, request, id, parameters, body)
	}
	writer := io.Writer(&h.streamBody)
	if h.discardStream {
		writer = io.Discard
	}
	h.streamSize, _ = io.Copy(writer, body)
	return h.streamActionErr
}
func (h *filesHost) StreamQuery(_ context.Context, id string, parameters map[string]string) (broker.StreamResult, error) {
	h.streamCalls++
	h.streamID, h.parameters = id, parameters
	if h.streamErr != nil {
		return h.stream, h.streamErr
	}
	return h.stream, nil
}

func serveFiles(t *testing.T, host *filesHost, method, target string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	New().Mount(mux, host)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(method, target, body))
	return response
}

func TestFilesPageRequiresAdministratorWithoutBrokerCall(t *testing.T) {
	host := &filesHost{}

	response := serveFiles(t, host, http.MethodGet, "/files/safe", nil)

	assert.Zero(t, host.queryCalls)
	assert.Zero(t, host.streamCalls)
	assert.Contains(t, response.Body.String(), "Administrator access required")
}

func TestFilesRootRedirectsToFirstSortedRoot(t *testing.T) {
	host := &filesHost{admin: true, state: State{Roots: []Root{{ID: "zeta"}, {ID: "alpha"}}}}

	response := serveFiles(t, host, http.MethodGet, "/files", nil)

	assert.Equal(t, http.StatusSeeOther, response.Code)
	assert.Equal(t, "/files/alpha?direction=asc&filter=&hidden=false&path=&sort=name", response.Header().Get("Location"))
	assert.Equal(t, broker.QueryFilesList, host.queryID)
	assert.Equal(t, map[string]string{"root": "", "path": "", "filter": "", "sort": "name", "direction": "asc", "hidden": "false"}, host.parameters)
}

func TestFilesRootRendersNoRoots(t *testing.T) {
	host := &filesHost{admin: true, state: State{}}

	response := serveFiles(t, host, http.MethodGet, "/files", nil)

	assert.Equal(t, http.StatusOK, response.Code)
	assert.Contains(t, response.Body.String(), "No file roots are configured.")
}

func TestFilesPageDispatchesOnlyFixedListQuery(t *testing.T) {
	host := &filesHost{admin: true, state: filesViewState()}

	response := serveFiles(t, host, http.MethodGet, "/files/safe?path=logs&filter=err&sort=size&direction=desc&hidden=true", nil)

	assert.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, broker.QueryFilesList, host.queryID)
	assert.Equal(t, map[string]string{"root": "safe", "path": "logs", "filter": "err", "sort": "size", "direction": "desc", "hidden": "true"}, host.parameters)
}

func TestFilesPageRendersUnavailableWithoutRawError(t *testing.T) {
	host := &filesHost{admin: true, queryErr: errors.New("private broker detail")}

	response := serveFiles(t, host, http.MethodGet, "/files/safe", nil)

	assert.Equal(t, http.StatusOK, response.Code)
	assert.Contains(t, response.Body.String(), "Files are temporarily unavailable.")
	assert.NotContains(t, response.Body.String(), "private broker detail")
}

func TestFilesPageRendersMissingStateForNotFoundQuery(t *testing.T) {
	host := &filesHost{admin: true, queryErr: broker.NewPublicError(http.StatusNotFound, "private", "not_found", errors.New("detail"))}

	response := serveFiles(t, host, http.MethodGet, "/files/safe", nil)

	assert.Equal(t, http.StatusOK, response.Code)
	assert.Contains(t, response.Body.String(), "This directory is inaccessible or no longer exists.")
	assert.NotContains(t, response.Body.String(), "Files are temporarily unavailable.")
}

func TestFilesDownloadStreamsExactResponse(t *testing.T) {
	body := io.NopCloser(strings.NewReader("payload-extra"))
	host := &filesHost{admin: true, stream: broker.StreamResult{Body: body, Filename: "report.txt", MediaType: "text/plain", Size: 7}}

	response := serveFiles(t, host, http.MethodGet, "/files/safe/download?path=logs/report.txt", nil)

	assert.Equal(t, http.StatusOK, response.Code)
	assert.Equal(t, broker.QueryFilesDownload, host.streamID)
	assert.Equal(t, map[string]string{"root": "safe", "path": "logs/report.txt"}, host.parameters)
	assert.Equal(t, "attachment; filename=report.txt", response.Header().Get("Content-Disposition"))
	assert.Equal(t, "application/octet-stream", response.Header().Get("Content-Type"))
	assert.Equal(t, "nosniff", response.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "7", response.Header().Get("Content-Length"))
	assert.Equal(t, "payload", response.Body.String())
}

func TestFilesDownloadClosesBodyReturnedWithError(t *testing.T) {
	body := &trackingBody{Reader: strings.NewReader("payload")}
	host := &filesHost{admin: true, stream: broker.StreamResult{Body: body}, streamErr: errors.New("private")}

	response := serveFiles(t, host, http.MethodGet, "/files/safe/download?path=report.txt", nil)

	assert.Equal(t, http.StatusServiceUnavailable, response.Code)
	assert.True(t, body.closed)
}

func TestFilesDownloadRejectsRangeWithoutBrokerCall(t *testing.T) {
	host := &filesHost{admin: true}
	requestBody := bytes.NewReader(nil)
	response := httptest.NewRecorder()
	mux := http.NewServeMux()
	New().Mount(mux, host)
	request := httptest.NewRequest(http.MethodGet, "/files/safe/download?path=report.txt", requestBody)
	request.Header.Set("Range", "bytes=0-1")
	mux.ServeHTTP(response, request)

	assert.Equal(t, http.StatusRequestedRangeNotSatisfiable, response.Code)
	assert.Zero(t, host.streamCalls)
}

func TestFilesDownloadMapsFailureBeforeWritingHeaders(t *testing.T) {
	host := &filesHost{admin: true, streamErr: broker.NewPublicError(http.StatusNotFound, "private", "not_found", errors.New("detail"))}

	response := serveFiles(t, host, http.MethodGet, "/files/safe/download?path=missing.txt", nil)

	assert.Equal(t, http.StatusNotFound, response.Code)
	assert.Empty(t, response.Header().Get("Content-Disposition"))
	assert.NotContains(t, response.Body.String(), "private")
}

func TestFilesDownloadRejectsInvalidSizeBeforeWritingHeaders(t *testing.T) {
	for _, size := range []int64{-1, MaxTransferBytes + 1} {
		t.Run(strconv.FormatInt(size, 10), func(t *testing.T) {
			host := &filesHost{admin: true, stream: broker.StreamResult{Body: io.NopCloser(strings.NewReader("payload")), Filename: "report.txt", Size: size}}

			response := serveFiles(t, host, http.MethodGet, "/files/safe/download?path=report.txt", nil)

			assert.Equal(t, http.StatusBadGateway, response.Code)
			assert.Empty(t, response.Header().Get("Content-Disposition"))
			assert.Empty(t, response.Header().Get("Content-Length"))
		})
	}
}

func multipartBody(parts ...string) (io.Reader, string) {
	const boundary = "task-nine-boundary"
	return strings.NewReader("--" + boundary + "\r\n" + strings.Join(parts, "\r\n--"+boundary+"\r\n") + "\r\n--" + boundary + "--\r\n"), "multipart/form-data; boundary=" + boundary
}

func multipartField(name, value string) string {
	return "Content-Disposition: form-data; name=\"" + name + "\"\r\n\r\n" + value
}

func multipartFile(name, filename, value string) string {
	return "Content-Disposition: form-data; name=\"" + name + "\"; filename=\"" + filename + "\"\r\nContent-Type: application/octet-stream\r\n\r\n" + value
}

func serveUpload(t *testing.T, host *filesHost, body io.Reader, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	New().Mount(mux, host)
	request := httptest.NewRequest(http.MethodPost, "/files/write/upload?path=reports", body)
	request.Header.Set("Content-Type", contentType)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}

func TestUploadRejectsMissingCSRFBeforeBrokerCall(t *testing.T) {
	host := &filesHost{admin: true, csrf: "csrf", validCSRF: true, discardStream: true}
	body, contentType := multipartBody(multipartFile("file", "report.txt", "payload"))

	response := serveUpload(t, host, body, contentType)

	assert.Equal(t, http.StatusBadRequest, response.Code)
	assert.Zero(t, host.csrfCalls)
	assert.Zero(t, host.streamCalls)
}

func TestUploadRejectsWrongCSRFFromInitialPartWithoutBrokerCall(t *testing.T) {
	host := &filesHost{admin: true, csrf: "csrf"}
	prefix := "--boundary\r\n" + multipartField("csrf", "wrong") + "\r\n--boundary\r\n"
	body := &afterPrefixReader{prefix: []byte(prefix)}

	response := serveUpload(t, host, body, "multipart/form-data; boundary=boundary")

	assert.Equal(t, 1, host.csrfCalls)
	assert.Zero(t, host.streamCalls)
	assert.False(t, body.readAfterPrefix)
	assert.Equal(t, http.StatusOK, response.Code)
}

func TestUploadStreamsOneFinalPartWithExactMetadata(t *testing.T) {
	host := &filesHost{admin: true, csrf: "csrf", validCSRF: true}
	body, contentType := multipartBody(multipartField("csrf", "csrf"), multipartFile("file", "report.txt", "payload"))

	response := serveUpload(t, host, body, contentType)

	assert.Equal(t, http.StatusSeeOther, response.Code)
	assert.Equal(t, broker.ActionFilesUpload, host.streamActionID)
	assert.Equal(t, map[string]string{"root": "write", "directory": "reports", "name": "report.txt"}, host.streamParameters)
	assert.Equal(t, "payload", host.streamBody.String())
	assert.Contains(t, response.Header().Get("Location"), "notice=Uploaded+report.txt")
}

func TestUploadRejectsFileBeforeCSRF(t *testing.T) {
	host := &filesHost{admin: true, csrf: "csrf", validCSRF: true}
	body, contentType := multipartBody(multipartFile("file", "report.txt", "payload"), multipartField("csrf", "csrf"))

	response := serveUpload(t, host, body, contentType)

	assert.Equal(t, http.StatusBadRequest, response.Code)
	assert.Zero(t, host.csrfCalls)
	assert.Zero(t, host.streamCalls)
}

func TestUploadRejectsTrailingMultipartPart(t *testing.T) {
	host := &filesHost{admin: true, csrf: "csrf", validCSRF: true}
	body, contentType := multipartBody(multipartField("csrf", "csrf"), multipartFile("file", "report.txt", "payload"), multipartField("extra", "value"))

	response := serveUpload(t, host, body, contentType)

	assert.Equal(t, http.StatusBadRequest, response.Code)
	assert.Equal(t, 1, host.streamCalls)
}

func TestUploadRejectsInvalidFilenameBeforeBrokerCall(t *testing.T) {
	host := &filesHost{admin: true, csrf: "csrf", validCSRF: true}
	body, contentType := multipartBody(multipartField("csrf", "csrf"), multipartFile("file", `dir\\report.txt`, "payload"))

	response := serveUpload(t, host, body, contentType)

	assert.Equal(t, http.StatusBadRequest, response.Code)
	assert.Zero(t, host.streamCalls)
}

func TestUploadRejectsUnsafeNamesBeforeBrokerCall(t *testing.T) {
	for _, name := range []string{"", ".", "..", "bad\x01name"} {
		t.Run(strconv.Quote(name), func(t *testing.T) {
			host := &filesHost{admin: true, csrf: "csrf", validCSRF: true}
			body, contentType := multipartBody(multipartField("csrf", "csrf"), multipartFile("file", name, "payload"))

			response := serveUpload(t, host, body, contentType)

			assert.Equal(t, http.StatusBadRequest, response.Code)
			assert.Zero(t, host.streamCalls)
		})
	}
}

func TestUploadMapsBrokerFailuresToStableNotices(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		notice string
	}{
		{"conflict", broker.NewPublicError(http.StatusConflict, "private", "conflict", errors.New("detail")), "A file with that name already exists."},
		{"read only", broker.NewPublicError(http.StatusForbidden, "private", "read_only", errors.New("detail")), "Uploads are disabled for this root."},
		{"unknown", errors.New("private broker detail"), "Upload failed. Review Activity for the recorded outcome."},
	} {
		t.Run(test.name, func(t *testing.T) {
			host := &filesHost{admin: true, csrf: "csrf", validCSRF: true, streamActionErr: test.err}
			body, contentType := multipartBody(multipartField("csrf", "csrf"), multipartFile("file", "report.txt", "payload"))

			response := serveUpload(t, host, body, contentType)

			assert.Equal(t, http.StatusSeeOther, response.Code)
			assert.Contains(t, response.Header().Get("Location"), url.QueryEscape(test.notice))
			assert.NotContains(t, response.Body.String(), "private broker detail")
		})
	}
}

func TestUploadUsesHXRedirectForHTMX(t *testing.T) {
	host := &filesHost{admin: true, csrf: "csrf", validCSRF: true}
	body, contentType := multipartBody(multipartField("csrf", "csrf"), multipartFile("file", "report.txt", "payload"))
	mux := http.NewServeMux()
	New().Mount(mux, host)
	request := httptest.NewRequest(http.MethodPost, "/files/write/upload?path=reports", body)
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("HX-Request", "true")
	response := httptest.NewRecorder()

	mux.ServeHTTP(response, request)

	assert.Equal(t, http.StatusNoContent, response.Code)
	assert.Contains(t, response.Header().Get("HX-Redirect"), "notice=Uploaded+report.txt")
}

func TestUploadRejectsFileOverTransferLimit(t *testing.T) {
	host := &filesHost{admin: true, csrf: "csrf", validCSRF: true, discardStream: true}
	const boundary = "large-upload-boundary"
	prefix := "--" + boundary + "\r\n" + multipartField("csrf", "csrf") + "\r\n--" + boundary + "\r\n" + multipartFile("file", "report.txt", "")
	suffix := "\r\n--" + boundary + "--\r\n"
	body := io.MultiReader(strings.NewReader(prefix), io.LimitReader(zeroReader{}, MaxTransferBytes+1), strings.NewReader(suffix))

	response := serveUpload(t, host, body, "multipart/form-data; boundary="+boundary)

	assert.Equal(t, http.StatusRequestEntityTooLarge, response.Code)
	assert.Equal(t, int64(MaxTransferBytes+1), host.streamSize)
}

func TestUploadMapsEarlyBrokerRejectionAfterPipeError(t *testing.T) {
	for attempt := 0; attempt < 100; attempt++ {
		host := &filesHost{admin: true, csrf: "csrf", validCSRF: true}
		host.streamAction = func(context.Context, *http.Request, string, map[string]string, io.Reader) error {
			return broker.NewPublicError(http.StatusForbidden, "private", "read_only", errors.New("rejected"))
		}
		body, contentType := multipartBody(multipartField("csrf", "csrf"), multipartFile("file", "report.txt", strings.Repeat("x", 8<<10)))

		response := serveUpload(t, host, body, contentType)

		assert.Equal(t, http.StatusSeeOther, response.Code)
		assert.Contains(t, response.Header().Get("Location"), url.QueryEscape("Uploads are disabled for this root."))
	}
}

type afterPrefixReader struct {
	prefix          []byte
	readAfterPrefix bool
}

func (r *afterPrefixReader) Read(buffer []byte) (int, error) {
	if len(r.prefix) == 0 {
		r.readAfterPrefix = true
		return 0, errors.New("file body was read")
	}
	buffer[0] = r.prefix[0]
	r.prefix = r.prefix[1:]
	return 1, nil
}

type zeroReader struct{}

func (zeroReader) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = 0
	}
	return len(buffer), nil
}

type trackingBody struct {
	io.Reader
	closed bool
}

func (b *trackingBody) Close() error {
	b.closed = true
	return nil
}
