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

import { BriefcaseBusiness, FlaskConical, Upload } from "lucide-react";

/** Provider type and optional sizing classes for its decorative icon. */
export interface ProviderIconProps {
  type: string;
  className?: string;
}

function IBKRIcon({ className }: { className?: string }) {
  return (
    <svg
      aria-hidden="true"
      viewBox="0 0 24 24"
      className={className}
      fill="none"
    >
      <rect width="24" height="24" rx="4" fill="#050608" />
      <path
        d="M2.8 3.2h5.8L4.1 13.4l4.7 7.4H3.1L0 16.4V8.3L2.8 3.2Z"
        fill="#d10f1f"
      />
      <path
        d="M8.6 3.2h3.8L7.9 12l4.5 8.8H8.6L4 13.4 8.6 3.2Z"
        fill="#ef1b2d"
      />
      <circle cx="15.8" cy="11.4" r="3.6" fill="#ef1b2d" />
    </svg>
  );
}

function BinanceIcon({ className }: { className?: string }) {
  return (
    <svg
      aria-hidden="true"
      viewBox="0 0 24 24"
      className={className}
      fill="none"
    >
      <rect width="24" height="24" rx="4" fill="#0b0e11" />
      <g fill="#f0b90b">
        <path d="M12 3.2 15 6.2 12 9.2 9 6.2 12 3.2Z" />
        <path d="M6.2 9 9.2 12 6.2 15 3.2 12 6.2 9Z" />
        <path d="M17.8 9 20.8 12 17.8 15 14.8 12 17.8 9Z" />
        <path d="M12 14.8 15 17.8 12 20.8 9 17.8 12 14.8Z" />
        <path d="M12 9.4 14.6 12 12 14.6 9.4 12 12 9.4Z" />
      </g>
    </svg>
  );
}

function KrakenIcon({ className }: { className?: string }) {
  return (
    <svg
      aria-hidden="true"
      viewBox="0 0 24 24"
      className={className}
      fill="none"
    >
      <rect width="24" height="24" rx="4" fill="#fff" />
      <path
        d="M4.8 15.7v-3.4c0-4.3 3.2-7.4 7.2-7.4s7.2 3.1 7.2 7.4v3.4a1.9 1.9 0 1 1-3.8 0v-3.1a1.5 1.5 0 0 0-3 0v3.1a1.9 1.9 0 1 1-3.8 0v-3.1a1.5 1.5 0 0 0-3 0v3.1a1.9 1.9 0 1 1-3.8 0Z"
        fill="#5841d8"
      />
    </svg>
  );
}

function CoinbaseIcon({ className }: { className?: string }) {
  return (
    <svg
      aria-hidden="true"
      viewBox="0 0 24 24"
      className={className}
      fill="none"
    >
      <rect width="24" height="24" rx="4" fill="#0052ff" />
      <circle cx="12" cy="12" r="7.2" fill="#fff" />
      <circle cx="12" cy="12" r="3.35" fill="#0052ff" />
      <rect x="12" y="10.15" width="6.4" height="3.7" fill="#0052ff" />
    </svg>
  );
}

function AlpacaIcon({ className }: { className?: string }) {
  return (
    <svg
      aria-hidden="true"
      viewBox="0 0 24 24"
      className={className}
      fill="none"
    >
      <rect width="24" height="24" rx="4" fill="#ffd200" />
      <path
        d="M10.1 20V9.8c0-2.8 2.2-5 5-5 2.6 0 4.8 2.1 4.8 4.8V20h-3.2V9.7c0-.9-.7-1.7-1.7-1.7s-1.7.8-1.7 1.7V20h-3.2Z"
        fill="#fff"
      />
      <path d="M4.2 11.8c.5-2.4 2.5-4.1 5.1-4.1h3v4.1H4.2Z" fill="#fff" />
      <path d="M13.7 4.4 15.1 2l.9 2.8-1.1 1-1.2-1.4Z" fill="#fff" />
      <path d="M16.1 4.8 17.7 2.8l.5 2.9-1.3.9-.8-1.8Z" fill="#fff" />
      <rect
        x="8.9"
        y="9.4"
        width="1.8"
        height="0.75"
        rx="0.35"
        fill="#d8d8d8"
      />
    </svg>
  );
}

function OKXIcon({ className }: { className?: string }) {
  return (
    <svg
      aria-hidden="true"
      viewBox="0 0 24 24"
      className={className}
      fill="none"
    >
      <rect width="24" height="24" rx="4" fill="#000" />
      <g fill="#fff">
        <rect x="5.25" y="5.25" width="4.5" height="4.5" rx="0.8" />
        <rect x="14.25" y="5.25" width="4.5" height="4.5" rx="0.8" />
        <rect x="9.75" y="9.75" width="4.5" height="4.5" rx="0.8" />
        <rect x="5.25" y="14.25" width="4.5" height="4.5" rx="0.8" />
        <rect x="14.25" y="14.25" width="4.5" height="4.5" rx="0.8" />
      </g>
    </svg>
  );
}

function BybitIcon({ className }: { className?: string }) {
  return (
    <svg
      aria-hidden="true"
      viewBox="0 0 24 24"
      className={className}
      fill="none"
    >
      <rect width="24" height="24" rx="4" fill="#151827" />
      <text
        x="11.2"
        y="13.9"
        textAnchor="middle"
        fontFamily="Arial, sans-serif"
        fontSize="6.1"
        fontWeight="800"
        fill="#fff"
      >
        BYB
      </text>
      <rect
        x="14.8"
        y="7.25"
        width="1.35"
        height="9.5"
        rx="0.15"
        fill="#f7a600"
      />
      <text
        x="18.65"
        y="13.9"
        textAnchor="middle"
        fontFamily="Arial, sans-serif"
        fontSize="6.1"
        fontWeight="800"
        fill="#fff"
      >
        T
      </text>
    </svg>
  );
}

function OANDAIcon({ className }: { className?: string }) {
  return (
    <svg
      aria-hidden="true"
      viewBox="0 0 24 24"
      className={className}
      fill="none"
    >
      <rect width="24" height="24" rx="4" fill="#fff" />
      <circle cx="6.4" cy="12" r="3.4" stroke="#111" strokeWidth="1.35" />
      <text
        x="14.2"
        y="13.7"
        textAnchor="middle"
        fontFamily="Arial, sans-serif"
        fontSize="5.6"
        fontWeight="700"
        fill="#111"
      >
        ANDA
      </text>
    </svg>
  );
}

function FinnhubIcon({ className }: { className?: string }) {
  return (
    <svg
      aria-hidden="true"
      viewBox="0 0 24 24"
      className={className}
      fill="none"
    >
      <rect width="24" height="24" rx="4" fill="#fff" />
      <path
        d="M8.2 16.7h10.9c1.5 0 2.7-1.2 2.7-2.7 0-1.4-1.1-2.6-2.5-2.7-.5-3.3-3.3-5.8-6.7-5.8-3.8 0-6.8 3-6.8 6.8v.1h2.4v4.3Z"
        fill="#37b34a"
      />
      <rect x="2.2" y="8.9" width="10.3" height="2.1" rx="1.05" fill="#fff" />
      <rect x="2.2" y="12.1" width="8.3" height="2" rx="1" fill="#fff" />
      <rect x="2.2" y="15.1" width="5.2" height="1.9" rx="0.95" fill="#fff" />
    </svg>
  );
}

/** Renders a provider brand icon; unknown types use the briefcase icon. */
export function ProviderIcon({ type, className }: ProviderIconProps) {
  if (type === "ib") {
    return <IBKRIcon className={className} />;
  }
  if (type === "binance") {
    return <BinanceIcon className={className} />;
  }
  if (type === "kraken") {
    return <KrakenIcon className={className} />;
  }
  if (type === "coinbase") {
    return <CoinbaseIcon className={className} />;
  }
  if (type === "alpaca") {
    return <AlpacaIcon className={className} />;
  }
  if (type === "okx") {
    return <OKXIcon className={className} />;
  }
  if (type === "bybit") {
    return <BybitIcon className={className} />;
  }
  if (type === "oanda") {
    return <OANDAIcon className={className} />;
  }
  if (type === "finnhub") {
    return <FinnhubIcon className={className} />;
  }
  if (type === "mock") {
    return <FlaskConical className={className} />;
  }
  if (type === "byo") {
    return <Upload className={className} />;
  }
  return <BriefcaseBusiness className={className} />;
}
