package serve

import (
	"errors"

	commonv1 "github.com/felag-engineering/gleipnir/plugin-sdk/gen/gleipnir/plugin/common/v1"
	"github.com/felag-engineering/gleipnir/plugin-sdk/pluginerr"
)

// errorToEnvelope converts an error returned by an ergonomic service method
// into a *commonv1.ErrorEnvelope for embedding in a response message.
//
// Mapping rules:
//   - an error chain containing a *pluginerr.CodedError → that code's
//     commonv1.ErrorCode 1:1 (CodeInvalidArg→ERROR_CODE_INVALID_ARG, …)
//   - any other error → ERROR_CODE_INTERNAL with Message = err.Error()
//
// errors.As walks the wrap chain, so a CodedError wrapped with fmt.Errorf
// ("...: %w", pluginerr.InvalidArg(...)) is still mapped to its real code
// rather than silently downgraded to INTERNAL.
//
// nil is never passed here; callers guard on err != nil before calling.
func errorToEnvelope(err error) *commonv1.ErrorEnvelope {
	var ce *pluginerr.CodedError
	if errors.As(err, &ce) {
		return &commonv1.ErrorEnvelope{
			Code:    pluginerrCodeToProto(ce.Code),
			Message: ce.Message,
		}
	}
	return &commonv1.ErrorEnvelope{
		Code:    commonv1.ErrorCode_ERROR_CODE_INTERNAL,
		Message: err.Error(),
	}
}

// pluginerrCodeToProto maps a pluginerr.Code to its proto counterpart.
// The numeric values are intentionally kept in sync (see pluginerr package),
// so this is a direct cast — but we keep the explicit switch to make the
// mapping readable and catch any future divergence at compile time.
func pluginerrCodeToProto(c pluginerr.Code) commonv1.ErrorCode {
	switch c {
	case pluginerr.CodeInternal:
		return commonv1.ErrorCode_ERROR_CODE_INTERNAL
	case pluginerr.CodeInvalidArg:
		return commonv1.ErrorCode_ERROR_CODE_INVALID_ARG
	case pluginerr.CodeNotFound:
		return commonv1.ErrorCode_ERROR_CODE_NOT_FOUND
	case pluginerr.CodeUnavailable:
		return commonv1.ErrorCode_ERROR_CODE_UNAVAILABLE
	case pluginerr.CodePermission:
		return commonv1.ErrorCode_ERROR_CODE_PERMISSION
	case pluginerr.CodeRateLimited:
		return commonv1.ErrorCode_ERROR_CODE_RATE_LIMITED
	case pluginerr.CodeUnimplemented:
		return commonv1.ErrorCode_ERROR_CODE_UNIMPLEMENTED
	default:
		// Unknown code — treat as internal rather than silently losing the error.
		return commonv1.ErrorCode_ERROR_CODE_INTERNAL
	}
}
