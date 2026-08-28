export function dashboardBasename(): string {
  return window.env?.DASHBOARD_BASENAME || '/dashboard';
}
