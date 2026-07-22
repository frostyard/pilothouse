//go:build !linux

package storage

import "errors"

var errArtifactsUnsupported = errors.New("remote storage artifacts are unsupported on this platform")

// ArtifactStore is available on every platform so callers can compile; artifact
// persistence is intentionally supported only on Linux.
type ArtifactStore struct {
	ManifestRoot   string
	CredentialRoot string
	UnitRoot       string
	UID            int
	GID            int
}

func NewArtifactStore() ArtifactStore                          { return ArtifactStore{} }
func (ArtifactStore) ManifestPath(string) (string, error)      { return "", errArtifactsUnsupported }
func (ArtifactStore) CredentialPath(string) (string, error)    { return "", errArtifactsUnsupported }
func (ArtifactStore) MountUnitPath(Definition) (string, error) { return "", errArtifactsUnsupported }
func (ArtifactStore) AutomountUnitPath(Definition) (string, error) {
	return "", errArtifactsUnsupported
}
func (ArtifactStore) WriteManifest(Definition) error { return errArtifactsUnsupported }
func (ArtifactStore) LoadDefinition(string) (Definition, error) {
	return Definition{}, errArtifactsUnsupported
}
func (ArtifactStore) WriteMountUnit(Definition) error               { return errArtifactsUnsupported }
func (ArtifactStore) WriteAutomountUnit(Definition) error           { return errArtifactsUnsupported }
func (ArtifactStore) WriteCredentials(string, string, string) error { return errArtifactsUnsupported }
func (ArtifactStore) VerifyOwnedArtifacts(Definition) error         { return errArtifactsUnsupported }
func RenderMountUnit(Definition) ([]byte, error)                    { return nil, errArtifactsUnsupported }
func RenderAutomountUnit(Definition) ([]byte, error)                { return nil, errArtifactsUnsupported }
func RenderCredentials(string, string) ([]byte, error)              { return nil, errArtifactsUnsupported }
