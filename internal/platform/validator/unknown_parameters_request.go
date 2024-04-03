package validator

import (
	"bytes"
	"encoding/csv"
	"io"
	"net/http"
	"strconv"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/routers"
	"github.com/pkg/errors"
	utils "github.com/savsgio/gotils/strconv"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fastjson"
)

// ErrUnknownQueryParameter is returned when a query parameter not defined in the OpenAPI specification.
var ErrUnknownQueryParameter = errors.New("query parameter not defined in the OpenAPI specification")

// ErrUnknownBodyParameter is returned when a body parameter not defined in the OpenAPI specification.
var ErrUnknownBodyParameter = errors.New("body parameter not defined in the OpenAPI specification")

// ErrDecodingFailed is returned when the API FW got error or unexpected value from the decoder
var ErrDecodingFailed = errors.New("the decoder returned the error")

// RequestParameterDetails contains details about found unknown parameter
type RequestParameterDetails struct {
	Name        string `json:"name"`
	Placeholder string `json:"location"`
	Type        string `json:"type"`
}

// RequestUnknownParameterError is returned by ValidateRequest when request does not match OpenAPI spec
type RequestUnknownParameterError struct {
	Parameters []RequestParameterDetails `json:"parameters"`
	Message    string                    `json:"message"`
}

// identifyData returns the type of the arg
func identifyData(data any) string {

	switch v := data.(type) {
	case int:
		return "integer"
	case string:
		// Try to parse as an integer
		if _, err := strconv.Atoi(v); err == nil {
			return "integer"
		}

		// Try to parse as a float
		if _, err := strconv.ParseFloat(v, 64); err == nil {
			return "float"
		}

		return "string"
	case float64, float32:
		return "float"
	case []byte:
		// Try to parse as an integer
		if _, err := strconv.Atoi(utils.B2S(v)); err == nil {
			return "integer"
		}

		// Try to parse as a float
		if _, err := strconv.ParseFloat(utils.B2S(v), 64); err == nil {
			return "float"
		}

		return "string"
	}

	return "unknown"
}

// ValidateUnknownRequestParameters is used to get a list of request parameters that are not specified in the OpenAPI specification
func ValidateUnknownRequestParameters(ctx *fasthttp.RequestCtx, route *routers.Route, header http.Header, jsonParser *fastjson.Parser) (foundUnknownParams []RequestUnknownParameterError, valError error) {

	operation := route.Operation
	operationParameters := operation.Parameters
	pathItemParameters := route.PathItem.Parameters

	// prepare a map with the list of params that defined in the OpenAPI specification
	specParams := make(map[string]*openapi3.Parameter)
	for _, parameterRef := range pathItemParameters {
		parameter := parameterRef.Value
		specParams[parameter.Name+parameter.In] = parameter
	}

	// add optional parameters to the map with parameters
	for _, parameterRef := range operationParameters {
		parameter := parameterRef.Value
		specParams[parameter.Name+parameter.In] = parameter
	}

	unknownQueryParams := RequestUnknownParameterError{}
	// compare list of all query params and list of params defined in the specification
	ctx.Request.URI().QueryArgs().VisitAll(func(key, value []byte) {
		if _, ok := specParams[utils.B2S(key)+openapi3.ParameterInQuery]; !ok {
			unknownQueryParams.Message = ErrUnknownQueryParameter.Error()
			unknownQueryParams.Parameters = append(unknownQueryParams.Parameters, RequestParameterDetails{
				Name:        utils.B2S(key),
				Placeholder: openapi3.ParameterInQuery,
				Type:        identifyData(utils.B2S(value)),
			})
		}
	})

	if unknownQueryParams.Message != "" {
		foundUnknownParams = append(foundUnknownParams, unknownQueryParams)
	}

	if operation.RequestBody == nil {
		return
	}

	// validate body params
	requestBody := operation.RequestBody.Value

	content := requestBody.Content
	if len(content) == 0 {
		// A request's body does not have declared content, so skip validation.
		return
	}

	if len(ctx.Request.Body()) == 0 {
		return foundUnknownParams, nil
	}

	// check post params
	inputMIME := string(ctx.Request.Header.ContentType())
	contentType := requestBody.Content.Get(inputMIME)
	if contentType == nil {
		return foundUnknownParams, nil
	}

	encFn := func(name string) *openapi3.Encoding { return contentType.Encoding[name] }
	mediaType, value, err := decodeBody(io.NopCloser(bytes.NewReader(ctx.Request.Body())), header, contentType.Schema, encFn, jsonParser)
	if err != nil {
		return foundUnknownParams, err
	}

	unknownBodyParams := RequestUnknownParameterError{}

	switch mediaType {
	case "text/plain":
		return nil, nil
	case "text/csv":
		r := csv.NewReader(io.NopCloser(bytes.NewReader(ctx.Request.Body())))

		record, err := r.Read()
		if err != nil {
			return foundUnknownParams, err
		}

		for _, rName := range record {
			if _, found := contentType.Schema.Value.Properties[rName]; !found {
				unknownBodyParams.Message = ErrUnknownBodyParameter.Error()
				unknownBodyParams.Parameters = append(unknownBodyParams.Parameters, RequestParameterDetails{
					Name:        rName,
					Placeholder: "body",
					Type:        identifyData(value),
				})
			}

		}
	case "application/x-www-form-urlencoded":
		// required params in paramList
		paramList, ok := value.(map[string]interface{})
		if !ok {
			return foundUnknownParams, ErrDecodingFailed
		}
		ctx.Request.PostArgs().VisitAll(func(key, value []byte) {
			if _, ok := paramList[string(key)]; !ok {
				unknownBodyParams.Message = ErrUnknownBodyParameter.Error()
				unknownBodyParams.Parameters = append(unknownBodyParams.Parameters, RequestParameterDetails{
					Name:        utils.B2S(key),
					Placeholder: "body",
					Type:        identifyData(value),
				})
			}
		})
	case "application/json", "application/xml", "multipart/form-data":
		paramList, ok := value.(map[string]interface{})
		if !ok {
			return foundUnknownParams, ErrDecodingFailed
		}
		for paramName, _ := range paramList {
			if _, found := contentType.Schema.Value.Properties[paramName]; !found {
				unknownBodyParams.Message = ErrUnknownBodyParameter.Error()
				unknownBodyParams.Parameters = append(unknownBodyParams.Parameters, RequestParameterDetails{
					Name:        paramName,
					Placeholder: "body",
					Type:        identifyData(paramList[paramName]),
				})
			}
		}
	default:
		return foundUnknownParams, ErrDecodingFailed
	}

	if unknownBodyParams.Message != "" {
		foundUnknownParams = append(foundUnknownParams, unknownBodyParams)
	}

	return
}
