import { homedir } from 'os';
import path from 'path';

type ServerImplementation = 'expo' | 'eoo';
export interface Credentials {
  token?: string;
  sessionSecret?: string;
}

export function detectServerImplementation(): ServerImplementation {
  return process.env.EOO_TOKEN ? 'eoo' : 'expo';
}

export function retrieveCredentials(): Credentials {
  const serverImplementation = detectServerImplementation();
  if (serverImplementation === 'eoo') {
    return {
      token: process.env.EOO_TOKEN,
    };
  }
  return retrieveExpoCredentials();
}

export function validateCredentials(credentials: Credentials): boolean {
  if (!credentials) {
    return false;
  }
  return !!(credentials.token || credentials.sessionSecret);
}

type SessionData = {
  sessionSecret: string;
  userId: string;
  username: string;
  currentConnection: 'Username-Password-Authentication' | 'Browser-Flow-Authentication';
};

function dotExpoHomeDirectory(): string {
  const home = homedir();
  if (!home) {
    throw new Error(
      "Can't determine your home directory; make sure your $HOME environment variable is set."
    );
  }

  let dirPath;
  if (process.env.EXPO_STAGING) {
    dirPath = path.join(home, '.expo-staging');
  } else if (process.env.EXPO_LOCAL) {
    dirPath = path.join(home, '.expo-local');
  } else {
    dirPath = path.join(home, '.expo');
  }
  return dirPath;
}

function getStateJsonPath(): string {
  return path.join(dotExpoHomeDirectory(), 'state.json');
}

function getExpoSessionData(): SessionData | null {
  try {
    const stateJsonPath = getStateJsonPath();
    const stateJson = require(stateJsonPath);
    return stateJson['auth'] || null;
  } catch {
    return null;
  }
}

export function retrieveExpoCredentials(): Credentials {
  const token = process.env.EXPO_TOKEN;
  const sessionData = getExpoSessionData();
  const sessionSecret = sessionData?.sessionSecret;
  return { token, sessionSecret };
}

export function getAuthHeaders(credentials: Credentials): Record<string, string> {
  if (credentials.token) {
    return {
      Authorization: `Bearer ${credentials.token}`,
    };
  }
  if (credentials?.sessionSecret) {
    return {
      'expo-session': credentials?.sessionSecret,
    };
  }
  return {};
}
