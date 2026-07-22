package files

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/platform"
)

func (*Module) Mount(mux *http.ServeMux, host platform.Host) {
	mux.HandleFunc("GET /files", func(w http.ResponseWriter, r *http.Request) {
		if !host.Identity(r).Admin {
			renderFilesDenied(w, r, host)
			return
		}
		filters := filesFilters(r, "", "")
		var state State
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		if host.Query(ctx, broker.QueryFilesList, filesListParameters(filters), &state) != nil {
			_ = host.Render(w, r, platform.Page{Active: "files", Body: Unavailable(false), Eyebrow: "configured storage", Title: "Files"})
			return
		}
		if len(state.Roots) == 0 {
			_ = host.Render(w, r, platform.Page{Active: "files", Body: Page(state, host.CSRFToken(r)), Eyebrow: "configured storage", Title: "Files"})
			return
		}
		sort.Slice(state.Roots, func(i, j int) bool { return state.Roots[i].ID < state.Roots[j].ID })
		http.Redirect(w, r, string(filesURL(state.Roots[0].ID, "", filters)), http.StatusSeeOther)
	})
	mux.HandleFunc("GET /files/{root}", func(w http.ResponseWriter, r *http.Request) {
		if !host.Identity(r).Admin {
			renderFilesDenied(w, r, host)
			return
		}
		filters := filesFilters(r, r.PathValue("root"), r.URL.Query().Get("path"))
		var state State
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		if err := host.Query(ctx, broker.QueryFilesList, filesListParameters(filters), &state); err != nil {
			_ = host.Render(w, r, platform.Page{Active: "files", Body: Unavailable(broker.StatusCode(err) == http.StatusNotFound), Eyebrow: "configured storage", Title: "Files"})
			return
		}
		state.Filters = filters
		_ = host.Render(w, r, platform.Page{Active: "files", Body: Page(state, host.CSRFToken(r)), Eyebrow: "configured storage", Title: "Files"})
	})
	mux.HandleFunc("GET /files/{root}/download", func(w http.ResponseWriter, r *http.Request) {
		if !host.Identity(r).Admin {
			renderFilesDenied(w, r, host)
			return
		}
		if r.Header.Get("Range") != "" {
			http.Error(w, "range requests are not supported", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
		defer cancel()
		result, err := host.StreamQuery(ctx, broker.QueryFilesDownload, map[string]string{"root": r.PathValue("root"), "path": r.URL.Query().Get("path")})
		if err != nil {
			if result.Body != nil {
				_ = result.Body.Close()
			}
			http.Error(w, "File download is unavailable.", broker.StatusCode(err))
			return
		}
		if result.Body == nil || result.Size < 0 || result.Size > MaxTransferBytes {
			if result.Body != nil {
				_ = result.Body.Close()
			}
			http.Error(w, "File download is unavailable.", http.StatusBadGateway)
			return
		}
		defer func() { _ = result.Body.Close() }()
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": result.Filename}))
		w.Header().Set("Content-Type", result.MediaType)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Length", strconv.FormatInt(result.Size, 10))
		w.WriteHeader(http.StatusOK)
		_, _ = io.CopyN(w, result.Body, result.Size)
	})
	mux.HandleFunc("POST /files/{root}/upload", func(w http.ResponseWriter, r *http.Request) {
		if !host.Identity(r).Admin {
			renderFilesDenied(w, r, host)
			return
		}
		handleUpload(w, r, host)
	})
}

func renderFilesDenied(w http.ResponseWriter, r *http.Request, host platform.Host) {
	_ = host.Render(w, r, platform.Page{Active: "files", Body: AccessDenied(), Eyebrow: "configured storage", Title: "Files"})
}

func filesFilters(r *http.Request, root, path string) ListRequest {
	query := r.URL.Query()
	return normalizeHTTPFilters(ListRequest{Root: root, Path: path, Filter: query.Get("filter"), Sort: query.Get("sort"), Direction: query.Get("direction"), Hidden: query.Get("hidden") == "true"})
}

func filesListParameters(filters ListRequest) map[string]string {
	return map[string]string{"root": filters.Root, "path": filters.Path, "filter": filters.Filter, "sort": filters.Sort, "direction": filters.Direction, "hidden": strconv.FormatBool(filters.Hidden)}
}

func uploadRedirect(root, directory, notice string) string {
	values := url.Values{"path": {directory}, "notice": {notice}}
	return fmt.Sprintf("/files/%s?%s", url.PathEscape(root), values.Encode())
}

func handleUpload(w http.ResponseWriter, r *http.Request, host platform.Host) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxTransferBytes+(1<<20))
	mediaType, parameters, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" || parameters["boundary"] == "" {
		http.Error(w, "invalid upload", http.StatusBadRequest)
		return
	}
	reader := multipart.NewReader(r.Body, parameters["boundary"])
	csrfPart, err := reader.NextPart()
	if err != nil || csrfPart.FormName() != "csrf" || csrfPart.FileName() != "" {
		http.Error(w, "invalid upload", http.StatusBadRequest)
		return
	}
	csrf, err := io.ReadAll(io.LimitReader(csrfPart, 4<<10+1))
	if err != nil || len(csrf) > 4<<10 || !host.ValidateActionToken(w, r, string(csrf)) {
		if err == nil && len(csrf) <= 4<<10 {
			return // ValidateActionToken supplied the stable authentication response.
		}
		http.Error(w, "invalid upload", http.StatusBadRequest)
		return
	}
	filePart, err := reader.NextPart()
	if err != nil || filePart.FormName() != "file" || !validUploadName(filePart.FileName()) {
		http.Error(w, "invalid upload", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()
	pipeReader, pipeWriter := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := host.StreamAction(ctx, r, broker.ActionFilesUpload, map[string]string{
			"root": r.PathValue("root"), "directory": r.URL.Query().Get("path"), "name": filePart.FileName(),
		}, pipeReader)
		_ = pipeReader.CloseWithError(err)
		done <- err
	}()

	n, copyErr := io.Copy(pipeWriter, io.LimitReader(filePart, MaxTransferBytes+1))
	if copyErr == nil && n > MaxTransferBytes {
		copyErr = errors.New("upload too large")
	}
	if copyErr == nil {
		_, copyErr = reader.NextPart()
		if errors.Is(copyErr, io.EOF) {
			copyErr = nil
		} else {
			if copyErr == nil {
				copyErr = errors.New("unexpected trailing multipart part")
			}
		}
	}
	if copyErr != nil {
		select {
		case brokerErr := <-done:
			if brokerErr != nil {
				uploadFailure(w, r, brokerErr)
				return
			}
		default:
		}
		_ = pipeWriter.CloseWithError(copyErr)
		<-done
		status := http.StatusBadRequest
		if n > MaxTransferBytes {
			status = http.StatusRequestEntityTooLarge
		}
		http.Error(w, "invalid upload", status)
		return
	}
	_ = pipeWriter.Close()
	if err := <-done; err != nil {
		uploadFailure(w, r, err)
		return
	}
	uploadSuccess(w, r, "Uploaded "+filePart.FileName())
}

func validUploadName(name string) bool {
	if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, `/\\`) {
		return false
	}
	for _, value := range name {
		if value < 0x20 || value == 0x7f {
			return false
		}
	}
	return true
}

func uploadFailure(w http.ResponseWriter, r *http.Request, err error) {
	notice := "Upload failed. Review Activity for the recorded outcome."
	switch broker.StatusCode(err) {
	case http.StatusConflict:
		notice = "A file with that name already exists."
	case http.StatusForbidden:
		notice = "Uploads are disabled for this root."
	}
	uploadSuccess(w, r, notice)
}

func uploadSuccess(w http.ResponseWriter, r *http.Request, notice string) {
	location := uploadRedirect(r.PathValue("root"), r.URL.Query().Get("path"), notice)
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", location)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, location, http.StatusSeeOther)
}
