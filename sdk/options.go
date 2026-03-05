package sdk

// Option configures a Client.
type Option func(*clientConfig)

type clientConfig struct {
	kubeconfig       string
	defaultNamespace string
}

// WithKubeconfig sets the path to the kubeconfig file.
// If not set, uses default discovery (~/.kube/config or in-cluster).
func WithKubeconfig(path string) Option {
	return func(c *clientConfig) {
		c.kubeconfig = path
	}
}

// WithNamespace sets the default namespace for operations.
// Can be overridden per-call via ClawSpec.Namespace.
func WithNamespace(ns string) Option {
	return func(c *clientConfig) {
		c.defaultNamespace = ns
	}
}
