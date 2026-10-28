package deployer

import (
	"errors"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation"
)

// publicStatusReasons limits public summaries to recognized API reasons.
var publicStatusReasons = sets.New(
	metav1.StatusReasonUnauthorized,
	metav1.StatusReasonForbidden,
	metav1.StatusReasonNotFound,
	metav1.StatusReasonAlreadyExists,
	metav1.StatusReasonConflict,
	metav1.StatusReasonInvalid,
	metav1.StatusReasonBadRequest,
	metav1.StatusReasonTimeout,
	metav1.StatusReasonServerTimeout,
	metav1.StatusReasonTooManyRequests,
	metav1.StatusReasonServiceUnavailable,
	metav1.StatusReasonInternalError,
	metav1.StatusReasonMethodNotAllowed,
	metav1.StatusReasonGone,
	metav1.StatusReasonRequestEntityTooLarge,
	metav1.StatusReasonUnsupportedMediaType,
)

type apiStatusError interface {
	error
	apierrors.APIStatus
}

// safeFailureMessage uses typed error fields only. Server messages,
// status causes, and formatted wrapper text never enter public output.
func safeFailureMessage(operation string, cause error) string {
	if statusError, ok := errors.AsType[apiStatusError](cause); ok {
		status := statusError.Status()
		if !publicStatusReasons.Has(status.Reason) {
			return operation
		}
		message := operation + ": " + string(status.Reason)
		if details := status.Details; details != nil {
			return withFailureIdentity(message, details.Group, details.Kind, details.Name)
		}
		return message
	}

	if mismatch, ok := errors.AsType[*meta.NoKindMatchError](cause); ok {
		return withFailureIdentity(operation+": NoMatch", mismatch.GroupKind.Group, mismatch.GroupKind.Kind, "")
	}
	if mismatch, ok := errors.AsType[*meta.NoResourceMatchError](cause); ok {
		return withFailureIdentity(operation+": NoMatch", mismatch.PartialResource.Group, mismatch.PartialResource.Resource, "")
	}
	return operation
}

func withFailureIdentity(message string, group string, kind string, name string) string {
	var identity []string
	for _, part := range []string{group, kind, name} {
		if part != "" && len(validation.IsDNS1123Subdomain(strings.ToLower(part))) == 0 {
			identity = append(identity, part)
		}
	}
	if len(identity) == 0 {
		return message
	}
	return message + " (" + strings.Join(identity, "/") + ")"
}
