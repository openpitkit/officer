// Copyright The Pit Project Owners. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// Please see https://officer.openpit.dev and the OWNERS file for details.

package httpapi

import (
	"context"
	"net/http"
	"time"

	"go.openpit.dev/officer/framework/backend"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

// SealReportSource answers when the credential store was sealed and how many
// secrets that resealed; sealed is false when it never was.
type SealReportSource func(ctx context.Context) (
	sealedAt time.Time, resealedSecrets int, sealed bool, err error,
)

// credentialStorageDTO is the REST answer of GET /credential-storage: the
// storage state the MCP tool also reports, and when the store was sealed with
// how many secrets that resealed. SealedAt is null and ResealedSecrets 0 for a
// store never sealed.
type credentialStorageDTO struct {
	httpx.CredentialStorageDTO
	SealedAt        *time.Time `json:"sealedAt"`
	ResealedSecrets int        `json:"resealedSecrets"`
}

// handleGetCredentialStorage handles GET /api/v1/credential-storage.
func handleGetCredentialStorage(
	svc backend.ControlPlane, sealReports SealReportSource,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		storage, err := svc.CredentialStorage(r.Context())
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		sealedAt, resealedSecrets, sealed, err := sealReports(r.Context())
		if err != nil {
			httpx.WriteErr(w, err)
			return
		}
		dto := credentialStorageDTO{
			CredentialStorageDTO: httpx.ToCredentialStorageDTO(storage),
		}
		if sealed {
			sealedAt = sealedAt.UTC()
			dto.SealedAt = &sealedAt
			dto.ResealedSecrets = resealedSecrets
		}
		httpx.WriteJSON(w, http.StatusOK, dto)
	}
}
