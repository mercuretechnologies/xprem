import { createContext, useContext } from 'react';

export type ThemePreference = 'light' | 'system' | 'dark';
export type ResolvedTheme = Exclude<ThemePreference, 'system'>;

const STORAGE_KEY = 'xprem-theme';
export const SYSTEM_DARK_QUERY = '(prefers-color-scheme: dark)';

const isThemePreference = (value: string | null): value is ThemePreference =>
  value === 'light' || value === 'system' || value === 'dark';

export const getStoredPreference = (): ThemePreference => {
  const stored = window.localStorage.getItem(STORAGE_KEY);
  return isThemePreference(stored) ? stored : 'system';
};

export const storePreference = (preference: ThemePreference) => {
  window.localStorage.setItem(STORAGE_KEY, preference);
};

export const resolveTheme = (preference: ThemePreference): ResolvedTheme => {
  if (preference !== 'system') return preference;
  return window.matchMedia(SYSTEM_DARK_QUERY).matches ? 'dark' : 'light';
};

export const applyTheme = (theme: ResolvedTheme) => {
  const root = document.documentElement;
  root.classList.toggle('light', theme === 'light');
  root.classList.toggle('dark', theme === 'dark');
  root.style.colorScheme = theme;
};

export type ThemeContextValue = {
  preference: ThemePreference;
  resolvedTheme: ResolvedTheme;
  setPreference: (preference: ThemePreference) => void;
};

export const ThemeContext = createContext<ThemeContextValue | null>(null);

export const useTheme = () => {
  const context = useContext(ThemeContext);
  if (!context) throw new Error('useTheme must be used within a ThemeProvider');
  return context;
};
