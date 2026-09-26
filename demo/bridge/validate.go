package main

import (
	"fmt"
	"strings"
)

// ApiError carries the HTTP status and machine code returned to clients.
type ApiError struct {
	Status  int
	Code    string
	Message string
}

func (e *ApiError) Error() string { return e.Message }

func validationError(format string, args ...any) *ApiError {
	return &ApiError{Status: 400, Code: "bad_request", Message: fmt.Sprintf(format, args...)}
}

func conflictError(code, format string, args ...any) *ApiError {
	return &ApiError{Status: 409, Code: code, Message: fmt.Sprintf(format, args...)}
}

func notFoundError(format string, args ...any) *ApiError {
	return &ApiError{Status: 404, Code: "not_found", Message: fmt.Sprintf(format, args...)}
}

func isValidationError(err error) bool {
	apiErr, ok := err.(*ApiError)
	return ok && apiErr.Status == 400 && apiErr.Code == "bad_request"
}

func requireObject(value any, name string) (map[string]any, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, validationError("%s must be an object", name)
	}
	return object, nil
}

func requireList(value any, name string) ([]any, error) {
	list, ok := value.([]any)
	if !ok {
		return nil, validationError("%s must be a list", name)
	}
	return list, nil
}

func requireSchema(body any) (map[string]any, error) {
	object, err := requireObject(body, "request")
	if err != nil {
		return nil, err
	}
	if version, ok := intValue(object["schema_version"]); !ok || version != SchemaVersion {
		return nil, validationError("schema_version must be 1")
	}
	return object, nil
}

func nonnegativeInt(value any, name string) (int64, error) {
	n, ok := intValue(value)
	if !ok || n < 0 {
		return 0, validationError("%s must be a nonnegative integer", name)
	}
	return n, nil
}

func integerOrNone(value any, name string) error {
	if value == nil {
		return nil
	}
	_, err := nonnegativeInt(value, name)
	return err
}

// finiteNumber validates a number; the returned pointer is nil only for an allowed null.
func finiteNumber(value any, name string, nullable bool, minimum *float64) (*float64, error) {
	if value == nil && nullable {
		return nil, nil
	}
	number, ok := numberValue(value)
	if !ok {
		suffix := ""
		if nullable {
			suffix = " or null"
		}
		return nil, validationError("%s must be finite%s", name, suffix)
	}
	if minimum != nil && number < *minimum {
		return nil, validationError("%s must be >= %g", name, *minimum)
	}
	return &number, nil
}

var zero = 0.0

func min0() *float64 { return &zero }

// observedNumber validates a required key and returns its original JSON value, preserving precision.
func observedNumber(object map[string]any, key, name string, nullable bool, minimum *float64) (any, *float64, error) {
	value, present := object[key]
	if !present {
		return nil, nil, validationError("%s.%s is required", name, key)
	}
	number, err := finiteNumber(value, name+"."+key, nullable, minimum)
	if err != nil {
		return nil, nil, err
	}
	return value, number, nil
}

func enumValue(value any, name string, allowed []string, nullable bool) (string, bool, error) {
	if value == nil && nullable {
		return "", false, nil
	}
	text, ok := value.(string)
	if ok {
		for _, candidate := range allowed {
			if text == candidate {
				return text, true, nil
			}
		}
	}
	return "", false, validationError("%s must be one of %s", name, strings.Join(allowed, ", "))
}
