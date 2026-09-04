/// <reference types="vite/client" />

interface Window {
  env?: {
    VITE_OTA_API_URL?: string;
    DASHBOARD_BASENAME?: string;
  };
}

