/*
Copyright 2025 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package handlers

import (
	"context"
	"encoding/json"
	"fmt"

	eppb "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/go-logr/logr"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/common"
)

// HandleResponseHeaders handles response headers.
func (s *Server) HandleResponseHeaders(headers *eppb.HttpHeaders) ([]*eppb.ProcessingResponse, error) {
	return []*eppb.ProcessingResponse{
		{
			Response: &eppb.ProcessingResponse_ResponseHeaders{
				ResponseHeaders: &eppb.HeadersResponse{},
			},
		},
	}, nil
}

// HandleResponseBody handles response bodies by executing response plugins in order.
func (s *Server) HandleResponseBody(ctx context.Context, responseBodyBytes []byte, logger logr.Logger) ([]*eppb.ProcessingResponse, error) {
	if len(s.responsePlugins) == 0 {
		return []*eppb.ProcessingResponse{
			{
				Response: &eppb.ProcessingResponse_ResponseBody{
					ResponseBody: &eppb.BodyResponse{},
				},
			},
		}, nil
	}

	var responseBody map[string]any
	if err := json.Unmarshal(responseBodyBytes, &responseBody); err != nil {
		logger.Error(err, "Failed to parse response body as JSON, skipping response plugins")
		return []*eppb.ProcessingResponse{
			{
				Response: &eppb.ProcessingResponse_ResponseBody{
					ResponseBody: &eppb.BodyResponse{},
				},
			},
		}, nil
	}

	if err := s.executePlugins(ctx, map[string]string{}, responseBody, s.responsePlugins); err != nil {
		logger.Error(err, "Response plugin execution failed")
		return nil, fmt.Errorf("failed to execute response plugins - %w", err)
	}

	// Re-marshal the (potentially mutated) response body.
	mutatedBytes, err := json.Marshal(responseBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal mutated response body - %w", err)
	}

	if s.streaming {
		var ret []*eppb.ProcessingResponse
		ret = addStreamedResponseBodyResponse(ret, mutatedBytes)
		return ret, nil
	}

	return []*eppb.ProcessingResponse{
		{
			Response: &eppb.ProcessingResponse_ResponseBody{
				ResponseBody: &eppb.BodyResponse{
					Response: &eppb.CommonResponse{
						BodyMutation: &eppb.BodyMutation{
							Mutation: &eppb.BodyMutation_Body{
								Body: mutatedBytes,
							},
						},
					},
				},
			},
		},
	}, nil
}

// HandleResponseTrailers handles response trailers.
func (s *Server) HandleResponseTrailers(trailers *eppb.HttpTrailers) ([]*eppb.ProcessingResponse, error) {
	return []*eppb.ProcessingResponse{
		{
			Response: &eppb.ProcessingResponse_ResponseTrailers{
				ResponseTrailers: &eppb.TrailersResponse{},
			},
		},
	}, nil
}

func addStreamedResponseBodyResponse(responses []*eppb.ProcessingResponse, responseBodyBytes []byte) []*eppb.ProcessingResponse {
	commonResponses := common.BuildChunkedBodyResponses(responseBodyBytes, true)
	for _, commonResp := range commonResponses {
		responses = append(responses, &eppb.ProcessingResponse{
			Response: &eppb.ProcessingResponse_ResponseBody{
				ResponseBody: &eppb.BodyResponse{
					Response: commonResp,
				},
			},
		})
	}
	return responses
}
