// Package k3sconfig defines the fixed local k3s connection contract shared
// by capability probing and the privileged inventory manager.
package k3sconfig

// KubeconfigPath is the server kubeconfig created by a standard k3s install.
// Pilothouse never accepts a caller-supplied kubeconfig path.
const KubeconfigPath = "/etc/rancher/k3s/k3s.yaml"
