package broker

import (
	"encoding/json"
	"io"
	"time"

	"github.com/frostyard/pilothouse/internal/auth"
)

const (
	ActionFilesUpload                      = "org.frostyard.pilothouse.files.upload"
	ActionDockerRemove                     = "org.frostyard.pilothouse.docker.remove"
	ActionDockerRemoveImage                = "org.frostyard.pilothouse.docker.remove_image"
	ActionDockerRestart                    = "org.frostyard.pilothouse.docker.restart"
	ActionDockerStart                      = "org.frostyard.pilothouse.docker.start"
	ActionDockerStop                       = "org.frostyard.pilothouse.docker.stop"
	ActionIncusRemove                      = "org.frostyard.pilothouse.incus.remove"
	ActionIncusRemoveImage                 = "org.frostyard.pilothouse.incus.remove_image"
	ActionIncusRestart                     = "org.frostyard.pilothouse.incus.restart"
	ActionIncusStart                       = "org.frostyard.pilothouse.incus.start"
	ActionIncusStop                        = "org.frostyard.pilothouse.incus.stop"
	ActionMaintenanceReboot                = "org.frostyard.pilothouse.maintenance.reboot"
	ActionPodmanRemove                     = "org.frostyard.pilothouse.podman.remove"
	ActionPodmanRemoveImage                = "org.frostyard.pilothouse.podman.remove_image"
	ActionPodmanRestart                    = "org.frostyard.pilothouse.podman.restart"
	ActionPodmanStart                      = "org.frostyard.pilothouse.podman.start"
	ActionPodmanStop                       = "org.frostyard.pilothouse.podman.stop"
	ActionSysextDisable                    = "org.frostyard.pilothouse.sysext.disable"
	ActionSysextEnable                     = "org.frostyard.pilothouse.sysext.enable"
	ActionSysextRefresh                    = "org.frostyard.pilothouse.sysext.refresh"
	ActionSysextUpdate                     = "org.frostyard.pilothouse.sysext.update"
	ActionServicesDisable                  = "org.frostyard.pilothouse.services.disable"
	ActionServicesEnable                   = "org.frostyard.pilothouse.services.enable"
	ActionServicesResetFailed              = "org.frostyard.pilothouse.services.reset_failed"
	ActionServicesRestart                  = "org.frostyard.pilothouse.services.restart"
	ActionServicesStart                    = "org.frostyard.pilothouse.services.start"
	ActionServicesStop                     = "org.frostyard.pilothouse.services.stop"
	ActionStorageCreateNFS                 = "org.frostyard.pilothouse.storage.create-nfs"
	ActionStorageCreateSMBGuest            = "org.frostyard.pilothouse.storage.create-smb-guest"
	ActionStorageCreateSMBCredentials      = "org.frostyard.pilothouse.storage.create-smb-credentials"
	ActionStorageCreateSMBGuestOwned       = "org.frostyard.pilothouse.storage.create-smb-guest-owned"
	ActionStorageCreateSMBCredentialsOwned = "org.frostyard.pilothouse.storage.create-smb-credentials-owned"
	ActionStorageMount                     = "org.frostyard.pilothouse.storage.mount"
	ActionStorageUnmount                   = "org.frostyard.pilothouse.storage.unmount"
	ActionStorageDelete                    = "org.frostyard.pilothouse.storage.delete"
)

const (
	QueryActivity         = "org.frostyard.pilothouse.activity.list"
	QueryBackupsState     = "org.frostyard.pilothouse.backups.state"
	QueryDockerLogs       = "org.frostyard.pilothouse.docker.logs"
	QueryDockerState      = "org.frostyard.pilothouse.docker.state"
	QueryIncusState       = "org.frostyard.pilothouse.incus.state"
	QueryJobs             = "org.frostyard.pilothouse.jobs.list"
	QueryLogs             = "org.frostyard.pilothouse.logs.list"
	QueryMaintenanceState = "org.frostyard.pilothouse.maintenance.state"
	QueryPodmanLogs       = "org.frostyard.pilothouse.podman.logs"
	QueryPodmanState      = "org.frostyard.pilothouse.podman.state"
	QueryServicesJournal  = "org.frostyard.pilothouse.services.journal"
	QueryServicesState    = "org.frostyard.pilothouse.services.state"
	QueryStorageState     = "org.frostyard.pilothouse.storage.state"
	QueryFilesDownload    = "org.frostyard.pilothouse.files.download"
	QueryFilesList        = "org.frostyard.pilothouse.files.list"
)

const (
	StreamMetadataHeader = "Pilothouse-Stream-Metadata"
	StreamNameHeader     = "Pilothouse-Stream-Name"
)

type ActionRequest struct {
	Confirmation string            `json:"confirmation,omitempty"`
	Parameters   map[string]string `json:"parameters"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

type LoginRequest struct {
	Password string `json:"password"`
	Remote   string `json:"remote"`
	Username string `json:"username"`
}

type LoginResponse struct {
	Session SessionResponse `json:"session"`
	Token   string          `json:"token"`
}

type QueryRequest struct {
	Parameters map[string]string `json:"parameters"`
}

type QueryResponse struct {
	Result json.RawMessage `json:"result"`
}

type SessionResponse struct {
	CSRF      string        `json:"csrf"`
	ExpiresAt time.Time     `json:"expires_at"`
	Identity  auth.Identity `json:"identity"`
}

type StreamResult struct {
	Body      io.ReadCloser
	Filename  string
	MediaType string
	Size      int64
}
