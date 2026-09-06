package tools

import (
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"

	"github.com/go-playground/locales/en"
	ut "github.com/go-playground/universal-translator"
	"github.com/go-playground/validator/v10"
	translations "github.com/go-playground/validator/v10/translations/en"
)

const ValidateRulesTagName = "v"

var (
	validate   *validator.Validate
	translator ut.Translator
)

func init() {
	// validator
	validate = validator.New(validator.WithRequiredStructEnabled())
	validate.SetTagName(ValidateRulesTagName)
	validate.RegisterTagNameFunc(func(field reflect.StructField) string {
		jsonTag := field.Tag.Get("json")
		if len(jsonTag) == 0 || jsonTag == "-" {
			return ""
		}
		return strings.
			ReplaceAll(jsonTag, ",omitempty", "")
	})
	// validation translations
	english := en.New()
	uni := ut.New(english, english)
	translator, _ = uni.GetTranslator("en")
	_ = translations.RegisterDefaultTranslations(validate, translator)
}

// ValidateStruct validates arbitrary struct
func ValidateStruct(s interface{}) []ValidationError {
	err := validate.Struct(s)
	if err == nil {
		return nil
	}
	var validationErrors validator.ValidationErrors
	if errors.As(err, &validationErrors) {
		vErrs := make([]ValidationError, 0, len(validationErrors))
		for _, vErr := range validationErrors {
			vErrs = append(vErrs, ValidationError{original: vErr})
		}
		return vErrs
	}
	slog.Error("validate.ValidateStruct error", slog.Any("error", err))
	return []ValidationError{{
		pointer: "UNKNOWN",
		detail:  err.Error(),
		code:    "INTERNAL_VALIDATION_ERROR",
	}}
}

// ValidateStructCompact calls ValidateStruct but returns single error instance
func ValidateStructCompact(s interface{}) error {
	return compactValidationErrors(ValidateStruct(s))
}

func compactValidationErrors(vErrs []ValidationError) error {
	if len(vErrs) > 0 {
		var line string
		for _, vErr := range vErrs {
			line += vErr.Detail() + ","
		}
		line = strings.TrimSuffix(line, ",")
		return fmt.Errorf("validation errors: %s", line)
	}
	return nil
}

// ---

type ValidationError struct {
	original validator.FieldError
	pointer  string
	detail   string
	code     string
}

func (e ValidationError) Pointer() string {
	if e.pointer != "" {
		return e.pointer
	}
	return "#/" + strings.ReplaceAll(e.original.Namespace(), ".", "/")
}

func (e ValidationError) Detail() string {
	if e.detail != "" {
		return e.detail
	}
	return e.original.Translate(translator)
}

func (e ValidationError) Code() string {
	if e.code != "" {
		return e.code
	}
	return "INVALID_VALUE"
}
