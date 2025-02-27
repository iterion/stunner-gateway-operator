package renderer

// ErrorType species the type of a non-critical rendering error
type ErrorType int

const (
	NoError ErrorType = iota

	// critical
	InvalidAuthType
	InvalidUsernamePassword
	InvalidSharedSecret
	InvalidDataplane
	NoRuleFound
	ExternalAuthCredentialsNotFound
	InvalidAuthConfig
	RenderingError
	InternalError

	// noncritical
	InvalidBackendGroup
	InvalidBackendKind
	BackendNotFound
	ServiceNotFound
	ClusterIPNotFound
	EndpointNotFound
	InconsitentClusterType
	InvalidProtocol
	PortUnavailable
	PublicAddressNotFound
)

type TypedError struct {
	reason ErrorType
}

// CriticalError is a fatal rendering error that prevents the rendering the dataplane config as a
// whole, or in parts.
type CriticalError struct {
	TypedError
}

// NewCriticalError creates a new fatal error.
func NewCriticalError(reason ErrorType) error {
	return &CriticalError{TypedError{reason: reason}}
}

// Error returns an error message.
func (e *CriticalError) Error() string {
	switch e.reason {
	case InvalidAuthType:
		return "invalid authentication type"
	case InvalidUsernamePassword:
		return "missing username and/or password for plaintext authentication"
	case InvalidSharedSecret:
		return "missing shared-secret for longterm authentication"
	case InvalidAuthConfig:
		return "internal error: could not validate generated auth config"
	case InvalidDataplane:
		return "missing Dataplane resource for Gateway"
	case NoRuleFound:
		return "no rules found in route"
	case ExternalAuthCredentialsNotFound:
		return "missing or invalid external authentication credentials"
	case RenderingError:
		return "could not render dataplane config"
	case InternalError:
		return "internal error"
	}
	return "Unknown error"
}

// NonCriticalError is a non-fatal error that affects a Gateway or a Route status: this is an event
// that is worth reporting but otherwise does not prevent the rendering of a valid dataplane
// config.
type NonCriticalError struct {
	TypedError
}

// NewNonCriticalError creates a new non-critical render error object.
func NewNonCriticalError(reason ErrorType) error {
	return &NonCriticalError{TypedError{reason: reason}}
}

// Error returns an error message.
func (e *NonCriticalError) Error() string {
	switch e.reason {
	case InvalidBackendGroup:
		return "invalid Group in backend reference (expecing: None)"
	case InvalidBackendKind:
		return "invalid Kind in backend reference (expecting Service)"
	case BackendNotFound:
		return "backend not found"
	case ServiceNotFound:
		return "no Service found for backend"
	case ClusterIPNotFound:
		return "no ClusterIP found for Service (this is fine for headless Services)"
	case EndpointNotFound:
		return "no Endpoint found for backend"
	case InconsitentClusterType:
		return "inconsitent cluster type for backends"
	case PortUnavailable:
		return "port unavailable"
	case InvalidProtocol:
		return "invalid protocol"
	case PublicAddressNotFound:
		return "no public address found for gateway"
	}
	return "Unknown error"
}

// IsCritical returns true of an error is critical.
func IsCritical(e error) bool {
	_, ok := e.(*CriticalError)
	return ok
}

// IsCriticalError returns true of an error is a critical error of the given type.
func IsCriticalError(e error, reason ErrorType) bool {
	err, ok := e.(*CriticalError)
	return ok && err.reason == reason
}

// IsNonCritical returns true of an error is critical.
func IsNonCritical(e error) bool {
	_, ok := e.(*NonCriticalError)
	return ok
}

// IsNonCriticalError returns true of an error is a critical error of the given type.
func IsNonCriticalError(e error, reason ErrorType) bool {
	err, ok := e.(*NonCriticalError)
	return err != nil && ok && err.reason == reason
}
