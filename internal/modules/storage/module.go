package storage

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/frostyard/pilothouse/internal/broker"
	"github.com/frostyard/pilothouse/internal/platform"
)

type Module struct{}

func New() *Module { return &Module{} }

func (*Module) Dashboard(ctx context.Context, host platform.Host) ([]platform.DashboardCard, error) {
	snapshot, err := queryState(ctx, host)
	if err != nil {
		return nil, err
	}
	return []platform.DashboardCard{{Component: SummaryCard(snapshot), Order: 25, Span: platform.SpanHalf}}, nil
}

func (*Module) Health(ctx context.Context, host platform.Host) ([]platform.Finding, error) {
	snapshot, err := queryState(ctx, host)
	if err != nil {
		return nil, err
	}
	findings := make([]platform.Finding, 0, len(snapshot.Findings))
	for _, finding := range snapshot.Findings {
		findings = append(findings, platform.Finding{
			Detail: finding.Detail, ID: "storage." + finding.ResourceID,
			Path: "/storage#" + storageAnchor(finding.ResourceID), Severity: storageSeverity(finding.Severity),
			Source: "Storage", Title: finding.Title,
		})
	}
	return findings, nil
}

func (*Module) Manifest() platform.Manifest {
	return platform.Manifest{ID: "storage", Name: "Storage", Path: "/storage", Icon: "disk", Order: 25}
}

func (*Module) Mount(mux *http.ServeMux, host platform.Host) {
	mux.HandleFunc("GET /storage", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		snapshot, err := queryState(ctx, host)
		_ = host.Render(w, r, platform.Page{Active: "storage", Body: ManagedPage(snapshot, err != nil, host.CSRFToken(r), host.Identity(r).Admin), Eyebrow: "Storage capacity", Title: "Storage"})
	})
	mux.HandleFunc("GET /storage/mounts/new", func(w http.ResponseWriter, r *http.Request) {
		if !host.Identity(r).Admin {
			http.Error(w, "administrator access required", http.StatusForbidden)
			return
		}
		protocol := r.URL.Query().Get("protocol")
		if protocol == "" {
			protocol = "nfs"
		}
		if !validRemoteProtocol(protocol) {
			http.NotFound(w, r)
			return
		}
		_ = host.Render(w, r, platform.Page{Active: "storage", Body: RemoteMountForm(protocol, host.CSRFToken(r)), Eyebrow: "Managed remote storage", Title: "Add remote mount"})
	})
	mux.HandleFunc("POST /storage/mounts", func(w http.ResponseWriter, r *http.Request) {
		if !host.Identity(r).Admin {
			http.Error(w, "administrator access required", http.StatusForbidden)
			return
		}
		if !host.ValidateAction(w, r) {
			return
		}
		if err := r.ParseForm(); err != nil {
			storageRedirect(w, r, "", err)
			return
		}
		action, parameters, success, err := remoteCreateAction(r.Form)
		if err == nil {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
			err = host.Execute(ctx, r, action, parameters)
			cancel()
		}
		storageRedirect(w, r, success, err)
	})
	mux.HandleFunc("POST /storage/mounts/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		if !host.Identity(r).Admin {
			http.Error(w, "administrator access required", http.StatusForbidden)
			return
		}
		if !host.ValidateAction(w, r) {
			return
		}
		id, action := r.PathValue("id"), r.PathValue("action")
		if ValidateDefinitionID(id) != nil {
			http.NotFound(w, r)
			return
		}
		actionID, success := remoteLifecycleAction(action)
		if actionID == "" {
			http.NotFound(w, r)
			return
		}
		if (action == "unmount" || action == "delete") && !host.ConfirmAction(w, r, fmt.Sprintf("%s remote mount", strings.ToUpper(action[:1])+action[1:]), "storage/mount/"+id) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		err := host.Execute(ctx, r, actionID, map[string]string{"id": id})
		cancel()
		storageRedirect(w, r, success, err)
	})
}

func validRemoteProtocol(protocol string) bool {
	return protocol == "nfs" || protocol == "smb-guest" || protocol == "smb-credentials"
}

func remoteCreateAction(form url.Values) (string, map[string]string, string, error) {
	protocol := strings.TrimSpace(form.Get("protocol"))
	parameters := map[string]string{
		"target":    strings.TrimSpace(form.Get("target")),
		"version":   strings.TrimSpace(form.Get("version")),
		"read_only": strings.TrimSpace(form.Get("read_only")),
	}
	if _, err := ParseReadOnly(parameters["read_only"]); err != nil || ValidateTarget(parameters["target"]) != nil {
		return "", nil, "", fmt.Errorf("invalid remote mount form")
	}
	switch protocol {
	case "nfs":
		parameters["host"] = strings.TrimSpace(form.Get("host"))
		parameters["export"] = strings.TrimSpace(form.Get("export"))
		if ValidateNFSHost(parameters["host"]) != nil || ValidateNFSExport(parameters["export"]) != nil || ValidateNFSVersion(parameters["version"]) != nil {
			return "", nil, "", fmt.Errorf("invalid remote mount form")
		}
		return broker.ActionStorageCreateNFS, parameters, "Remote mount added", nil
	case "smb-guest", "smb-credentials":
		parameters["server"] = strings.TrimSpace(form.Get("server"))
		parameters["share"] = strings.TrimSpace(form.Get("share"))
		if ValidateSMBServer(parameters["server"]) != nil || ValidateSMBShare(parameters["share"]) != nil || ValidateSMBVersion(parameters["version"]) != nil {
			return "", nil, "", fmt.Errorf("invalid remote mount form")
		}
		ownership, err := ParseSMBOwnership(strings.TrimSpace(form.Get("uid")), strings.TrimSpace(form.Get("gid")))
		if err != nil {
			return "", nil, "", fmt.Errorf("invalid remote mount form")
		}
		owned := ownership != (SMBOwnership{})
		if owned {
			parameters["uid"], parameters["gid"] = ownership.UID, ownership.GID
		}
		if protocol == "smb-credentials" {
			parameters["username"] = strings.TrimSpace(form.Get("username"))
			parameters["password"] = form.Get("password")
			if ValidateUsername(parameters["username"]) != nil || ValidatePassword(parameters["password"]) != nil {
				return "", nil, "", fmt.Errorf("invalid remote mount form")
			}
			if owned {
				return broker.ActionStorageCreateSMBCredentialsOwned, parameters, "Remote mount added", nil
			}
			return broker.ActionStorageCreateSMBCredentials, parameters, "Remote mount added", nil
		}
		if owned {
			return broker.ActionStorageCreateSMBGuestOwned, parameters, "Remote mount added", nil
		}
		return broker.ActionStorageCreateSMBGuest, parameters, "Remote mount added", nil
	default:
		return "", nil, "", fmt.Errorf("invalid remote mount form")
	}
}

func remoteLifecycleAction(action string) (string, string) {
	switch action {
	case "mount":
		return broker.ActionStorageMount, "Mount started"
	case "unmount":
		return broker.ActionStorageUnmount, "Mount unmounted"
	case "delete":
		return broker.ActionStorageDelete, "Mount deleted"
	default:
		return "", ""
	}
}

func storageRedirect(w http.ResponseWriter, r *http.Request, success string, err error) {
	values := url.Values{}
	if err != nil {
		values.Set("kind", "error")
		values.Set("notice", "Action failed. Review Activity for the recorded outcome.")
	} else {
		values.Set("notice", success)
	}
	destination := "/storage?" + values.Encode()
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", destination)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, destination, http.StatusSeeOther)
}

func queryState(ctx context.Context, host platform.Host) (Snapshot, error) {
	var snapshot Snapshot
	err := host.Query(ctx, broker.QueryStorageState, nil, &snapshot)
	return snapshot, err
}

func storageSeverity(health Health) platform.Severity {
	switch health {
	case HealthCritical:
		return platform.SeverityCritical
	case HealthWarning:
		return platform.SeverityWarning
	case HealthUnknown:
		return platform.SeverityUnknown
	case HealthHealthy:
		return platform.SeverityInfo
	default:
		return platform.SeverityUnknown
	}
}

func storageAnchor(resourceID string) string {
	return strings.Map(func(character rune) rune {
		if character <= unicode.MaxASCII && ((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9')) {
			return character
		}
		return '-'
	}, resourceID)
}
