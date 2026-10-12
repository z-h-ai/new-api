package common

// ClientRequestId is private caller correlation metadata, never an identity or
// an idempotency key. The gateway request ID remains independently generated.
const ClientRequestIdHeader = "X-Polo-Request-Id"
const ClientRequestIdKey = "client_request_id"

func IsValidClientRequestId(value string) bool {
	if len(value) != 32 {
		return false
	}
	for i := range len(value) {
		if !((value[i] >= '0' && value[i] <= '9') || (value[i] >= 'a' && value[i] <= 'f')) {
			return false
		}
	}
	return true
}
