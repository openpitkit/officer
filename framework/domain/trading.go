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
// Please see https://openpit.dev and the OWNERS file for details.

package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// TradingMode identifies the class of venue environment used by a trading
// connection.
type TradingMode string

const (
	// TradingModePaper identifies a paper, testnet or demo environment.
	TradingModePaper TradingMode = "paper"
	// TradingModeLive identifies a real-money environment.
	TradingModeLive TradingMode = "live"
)

// TradingProviderAlpaca is the Alpaca trading-provider discriminator.
const TradingProviderAlpaca = "alpaca"

// TradingModeSupported reports whether mode is a supported trading mode.
func TradingModeSupported(mode TradingMode) bool {
	return mode == TradingModePaper || mode == TradingModeLive
}

// TradingConnection describes one configured connection to a trading venue.
// Credentials is opaque connector-read JSON configuration, stored like
// market-data credentials: sealed at rest with the master key when configured,
// plaintext otherwise. Enabled gates new sends only; orders already sent
// continue to be tracked when the connection is disabled.
type TradingConnection struct {
	// ExternalID is the opaque public handle of this connection.
	ExternalID ExternalID
	// Provider is the trading-provider discriminator.
	Provider string
	// Label is the unique, case-insensitive operator-facing name.
	Label string
	// Mode identifies whether the venue environment is paper or live.
	Mode TradingMode
	// VenueIdentity identifies the venue-side account reached by the credentials.
	// A second connection to the same provider and venue identity is refused.
	VenueIdentity string
	// Credentials is opaque connector-read JSON configuration.
	Credentials string
	// Enabled reports whether new orders may be sent through the connection.
	Enabled bool
	// FeeAccount receives unlinked venue fees whole. Empty uses the single
	// Officer account that can send through the connection, directly or
	// through a granted group, when there is exactly one.
	FeeAccount AccountID
}

// TradingInstrument maps one venue symbol to one Officer instrument on a
// trading connection.
type TradingInstrument struct {
	// Connection is the owning connection's opaque public handle.
	Connection ExternalID
	// ExternalSymbol is the venue-side symbol.
	ExternalSymbol string
	// BaseAsset is the code of the instrument underlying asset.
	BaseAsset string
	// QuoteAsset is the code of the instrument settlement asset.
	QuoteAsset string
	// Enabled reports whether new orders may use this instrument mapping.
	Enabled bool
}

// TradingAccess grants one trading connection to one subject: an Officer
// account or an account group, exactly one of Account and AccountGroup. There
// is one grant per connection and subject. A group grant reaches every current
// member of the group; on one connection an account's own grant wins over its
// group's. VenueAccount is the optional bound venue-side account, empty when
// none.
type TradingAccess struct {
	// Account is the code of the Officer account receiving access; empty for
	// a group grant.
	Account AccountID
	// AccountGroup is the code of the account group receiving access; empty
	// for an account grant. The default group is never a subject.
	AccountGroup string
	// Connection is the trading connection the subject may use.
	Connection ExternalID
	// VenueAccount is the optional bound venue-side account, empty when none.
	VenueAccount string
}

// SameSubject reports whether a and other grant to the same subject: the same
// account or the same account group. It is the one comparison of grant
// subjects; comparing Account alone would equate any two group grants, since
// both carry an empty Account.
func (a TradingAccess) SameSubject(other TradingAccess) bool {
	return a.Account == other.Account && a.AccountGroup == other.AccountGroup
}

// SubjectLabel renders the grant's subject for audit details and error
// messages: the account code, or "group:<code>" for a group grant, so a group
// never reads as an account with the same code.
func (a TradingAccess) SubjectLabel() string {
	if a.AccountGroup != "" {
		return "group:" + a.AccountGroup
	}
	return a.Account.String()
}

// TradingDestination identifies where one Officer order is sent.
type TradingDestination struct {
	// Connection is the trading connection used for the order.
	Connection ExternalID
	// VenueAccount is the provider-specific venue-side account identifier.
	VenueAccount string
	// Route is opaque connector-read per-order JSON configuration.
	Route string
}

// VenueOrder is the durable link between an Officer order and the order sent
// to a venue. SendAttempted distinguishes a link that is still safe to abandon
// from one whose send may have reached the venue. An empty VenueOrderID means
// that the venue has not acknowledged the send.
type VenueOrder struct {
	// Order is the Officer order's opaque public handle.
	Order ExternalID
	// Connection is the trading connection used for the order.
	Connection ExternalID
	// VenueAccount is the provider-specific venue-side account identifier.
	VenueAccount string
	// Route is the connector-read per-order JSON used for the send.
	Route string
	// ClientOrderID is the idempotency identifier sent to the venue.
	ClientOrderID string
	// SendAttempted reports whether the venue call has been started.
	SendAttempted bool
	// VenueOrderID is the venue-assigned identifier, empty until acknowledged.
	VenueOrderID string
	// CreatedAt is when the durable link was created.
	CreatedAt time.Time
}

// ValidateTradingConnection validates a trading connection before storage.
func ValidateTradingConnection(connection TradingConnection) error {
	if connection.FeeAccount != "" {
		if err := ValidateAccountID(connection.FeeAccount); err != nil {
			return fmt.Errorf("trading connection fee account: %w", err)
		}
	}
	if err := validateCanonicalTradingString("venue identity", connection.VenueIdentity); err != nil {
		return err
	}
	if connection.VenueIdentity == "" {
		return fmt.Errorf("trading connection venue identity: %w", ErrInvalid)
	}
	if err := validateCanonicalTradingString("provider", connection.Provider); err != nil {
		return err
	}
	if strings.TrimSpace(string(connection.Mode)) != string(connection.Mode) {
		return fmt.Errorf("trading connection mode is not canonical: %w", ErrInvalid)
	}
	if connection.Provider == "" {
		return fmt.Errorf("trading connection provider: %w", ErrInvalid)
	}
	if !TradingModeSupported(connection.Mode) {
		return fmt.Errorf("trading connection mode %q: %w", connection.Mode, ErrInvalid)
	}
	return ValidateTradingConnectionSettings(connection.Label, connection.Credentials)
}

// ValidateTradingConnectionSettings requires a canonical, non-empty label and
// canonical credentials containing a JSON object. It returns ErrInvalid for
// invalid settings without exposing credentials in the error.
func ValidateTradingConnectionSettings(label, credentials string) error {
	if err := validateCanonicalTradingString("label", label); err != nil {
		return err
	}
	if label == "" {
		return fmt.Errorf("trading connection label: %w", ErrInvalid)
	}
	if credentials != strings.TrimSpace(credentials) {
		return fmt.Errorf("trading connection credentials are not canonical: %w", ErrInvalid)
	}
	if err := validateTradingJSONObject(credentials); err != nil {
		return fmt.Errorf("trading connection credentials: %w", ErrInvalid)
	}
	return nil
}

// ValidateTradingInstrument validates a trading instrument before storage.
func ValidateTradingInstrument(instrument TradingInstrument) error {
	if err := validateCanonicalTradingString("external symbol", instrument.ExternalSymbol); err != nil {
		return err
	}
	if err := validateCanonicalTradingString("base asset", instrument.BaseAsset); err != nil {
		return err
	}
	if err := validateCanonicalTradingString("quote asset", instrument.QuoteAsset); err != nil {
		return err
	}
	if instrument.Connection.IsZero() || instrument.ExternalSymbol == "" {
		return fmt.Errorf("trading instrument: %w", ErrInvalid)
	}
	if err := ValidateAsset(instrument.BaseAsset); err != nil {
		return err
	}
	if err := ValidateAsset(instrument.QuoteAsset); err != nil {
		return err
	}
	if instrument.BaseAsset == instrument.QuoteAsset {
		return fmt.Errorf("trading instrument assets must differ: %w", ErrInvalid)
	}
	return nil
}

// ValidateTradingAccess validates a grant before any lookup. Exactly one of
// Account and AccountGroup must be set; the account code must pass
// ValidateAccountID and the group code ValidateGroupID, which refuses the
// reserved "-", so the default group never becomes a subject.
func ValidateTradingAccess(access TradingAccess) error {
	switch {
	case access.Account == "" && access.AccountGroup == "":
		return invalidField(
			"/account", "required",
			fmt.Errorf(
				"trading access requires an account or an account group: %w",
				ErrInvalid,
			),
		)
	case access.Account != "" && access.AccountGroup != "":
		return invalidField(
			"/accountGroup", "unsupported_value",
			fmt.Errorf(
				"trading access must not have both an account and an account group: %w",
				ErrInvalid,
			),
		)
	case access.AccountGroup != "":
		if err := ValidateGroupID(access.AccountGroup); err != nil {
			return WithValidationPointer("/accountGroup", err)
		}
	default:
		if err := ValidateAccountID(access.Account); err != nil {
			return err
		}
	}
	if access.Connection.IsZero() {
		return fmt.Errorf("trading access connection: %w", ErrInvalid)
	}
	if err := validateCanonicalTradingString("venue account", access.VenueAccount); err != nil {
		return err
	}
	return nil
}

// ValidateTradingDestination validates a per-order trading destination.
func ValidateTradingDestination(destination TradingDestination) error {
	if destination.Connection.IsZero() {
		return fmt.Errorf("trading destination connection: %w", ErrInvalid)
	}
	if err := validateCanonicalTradingString("venue account", destination.VenueAccount); err != nil {
		return err
	}
	return ValidateTradingRoute(destination.Route)
}

// ValidateTradingRoute requires a JSON object without surrounding whitespace.
func ValidateTradingRoute(route string) error {
	if route != strings.TrimSpace(route) {
		return fmt.Errorf("trading destination route is not canonical: %w", ErrInvalid)
	}
	if err := validateTradingJSONObject(route); err != nil {
		return fmt.Errorf("trading destination route: %w", ErrInvalid)
	}
	return nil
}

// ValidateVenueOrder validates a durable venue-order link before storage.
func ValidateVenueOrder(order VenueOrder) error {
	if order.Order.IsZero() {
		return fmt.Errorf("venue order order: %w", ErrInvalid)
	}
	if err := ValidateTradingDestination(TradingDestination{
		Connection:   order.Connection,
		VenueAccount: order.VenueAccount,
		Route:        order.Route,
	}); err != nil {
		return err
	}
	if err := validateCanonicalTradingString(
		"client order id", order.ClientOrderID,
	); err != nil {
		return err
	}
	if err := validateCanonicalTradingString(
		"venue order id", order.VenueOrderID,
	); err != nil {
		return err
	}
	if order.ClientOrderID == "" {
		return fmt.Errorf("venue order client order id: %w", ErrInvalid)
	}
	if order.VenueOrderID != "" && !order.SendAttempted {
		return fmt.Errorf(
			"venue order acknowledgement without send attempt: %w", ErrInvalid,
		)
	}
	return nil
}

func validateCanonicalTradingString(field, value string) error {
	if value != strings.TrimSpace(value) {
		return fmt.Errorf("trading %s %q is not canonical: %w", field, value, ErrInvalid)
	}
	return nil
}

func validateTradingJSONObject(value string) error {
	if value == "" {
		return ErrInvalid
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(value), &object); err != nil || object == nil {
		return ErrInvalid
	}
	return nil
}
