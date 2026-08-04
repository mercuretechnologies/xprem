import { ControlCenter } from '@xprem/control-center';
import { Observe, ObserveRoot } from 'expo-observe';
import { DarkTheme, DefaultTheme, ThemeProvider } from 'expo-router/react-navigation';
import { useFonts } from 'expo-font';
import { Stack } from 'expo-router';
import * as SplashScreen from 'expo-splash-screen';
import { StatusBar } from 'expo-status-bar';
import { useEffect } from 'react';
import 'react-native-reanimated';

import { useColorScheme } from '@/hooks/useColorScheme';
import ErrorBoundary from '@/components/ErrorBounday';

// Prevent the splash screen from auto-hiding before asset loading is complete.
SplashScreen.preventAutoHideAsync();

function RootLayout() {
  const colorScheme = useColorScheme();
  const [loaded] = useFonts({
    SpaceMono: require('../assets/fonts/SpaceMono-Regular.ttf'),
  });

  useEffect(() => {
    // Once per JS session, not once per render.
    Observe.logEvent('app_started');
    Observe.dispatchEvents();
  }, []);

  useEffect(() => {
    if (loaded) {
      SplashScreen.hideAsync();
    }
  }, [loaded]);

  if (!loaded) {
    return null;
  }

  // markInteractive is deliberately not called here: the root layout is not a
  // screen, so the router integration would have no route to attribute the
  // metric to. Each screen calls it once it is actually usable.
  return (
    <ErrorBoundary>
      <ThemeProvider value={colorScheme === 'dark' ? DarkTheme : DefaultTheme}>
        <Stack>
          <Stack.Screen name="(tabs)" options={{ headerShown: false }} />
          <Stack.Screen
            name="modal"
            options={{ presentation: 'modal', title: 'Modal' }}
          />
          <Stack.Screen name="+not-found" />
        </Stack>
        <StatusBar style="auto" />
        {/* Renders nothing unless the channel this build polls allows branch
            surfing, so it is safe to leave mounted in every build. */}
        <ControlCenter />
      </ThemeProvider>
    </ErrorBoundary>
  );
}

export default ObserveRoot.wrap(RootLayout);
