import Ionicons from '@expo/vector-icons/Ionicons'
import { createBottomTabNavigator } from '@react-navigation/bottom-tabs'
import {
  DarkTheme,
  DefaultTheme,
  type NavigatorScreenParams,
} from '@react-navigation/native'
import {
  createNativeStackNavigator,
  type NativeStackScreenProps,
} from '@react-navigation/native-stack'
import { useFonts } from 'expo-font'
import { Observe, ObserveRoot } from 'expo-observe'
import { ObserveNavigationContainer } from 'expo-observe/integrations/react-navigation'
import * as SplashScreen from 'expo-splash-screen'
import { StatusBar } from 'expo-status-bar'
import { useEffect } from 'react'
import 'react-native-reanimated'

import ErrorBoundary from '@/components/ErrorBounday'
import { useColorScheme } from '@/hooks/useColorScheme'
import { CatalogScreen } from '@/screens/CatalogScreen'
import { ItemScreen } from '@/screens/ItemScreen'
import { LabScreen } from '@/screens/LabScreen'
import { ModalScreen } from '@/screens/ModalScreen'
import { SlowScreen } from '@/screens/SlowScreen'
import { UpdatesScreen } from '@/screens/UpdatesScreen'

// Mirrors the expo-router tree in app/. Screen names are what the integration
// joins into the reported pathname (`/Tabs/Catalog/Item`), so they are chosen
// to read like the router's route patterns.
export type CatalogStackParamList = {
  CatalogList: undefined
  Item: { id: string; from: string }
}

export type LabStackParamList = {
  LabHome: undefined
  Slow: undefined
}

export type TabsParamList = {
  Updates: undefined
  Catalog: NavigatorScreenParams<CatalogStackParamList> | undefined
  Lab: NavigatorScreenParams<LabStackParamList> | undefined
}

export type RootStackParamList = {
  Tabs: NavigatorScreenParams<TabsParamList> | undefined
  Modal: undefined
}

SplashScreen.preventAutoHideAsync()

const CatalogStack = createNativeStackNavigator<CatalogStackParamList>()
const LabStack = createNativeStackNavigator<LabStackParamList>()
const Tabs = createBottomTabNavigator<TabsParamList>()
const RootStack = createNativeStackNavigator<RootStackParamList>()

function CatalogNavigator() {
  return (
    <CatalogStack.Navigator>
      <CatalogStack.Screen
        name="CatalogList"
        options={{ title: 'Catalog' }}
        component={CatalogListRoute}
      />
      <CatalogStack.Screen
        name="Item"
        options={{ title: 'Item' }}
        component={ItemRoute}
      />
    </CatalogStack.Navigator>
  )
}

function CatalogListRoute({
  navigation,
}: NativeStackScreenProps<CatalogStackParamList, 'CatalogList'>) {
  return (
    <CatalogScreen
      onOpenItem={(id) => navigation.navigate('Item', { id, from: 'list' })}
    />
  )
}

function ItemRoute({
  route,
}: NativeStackScreenProps<CatalogStackParamList, 'Item'>) {
  return <ItemScreen id={route.params.id} params={route.params} />
}

function LabNavigator() {
  return (
    <LabStack.Navigator>
      <LabStack.Screen
        name="LabHome"
        options={{ title: 'Lab' }}
        component={LabRoute}
      />
      <LabStack.Screen
        name="Slow"
        options={{ title: 'Slow screen' }}
        component={SlowScreen}
      />
    </LabStack.Navigator>
  )
}

function LabRoute({
  navigation,
}: NativeStackScreenProps<LabStackParamList, 'LabHome'>) {
  return (
    <LabScreen
      onOpenSlow={() => navigation.navigate('Slow')}
      // The modal lives on the root stack, one level above the tabs.
      onOpenModal={() => navigation.getParent()?.getParent()?.navigate('Modal')}
    />
  )
}

function TabsNavigator() {
  return (
    <Tabs.Navigator screenOptions={{ headerShown: false }}>
      <Tabs.Screen
        name="Updates"
        component={UpdatesScreen}
        options={{
          tabBarIcon: ({ color, size }) => (
            <Ionicons name="cloud-download-outline" color={color} size={size} />
          ),
        }}
      />
      <Tabs.Screen
        name="Catalog"
        component={CatalogNavigator}
        options={{
          tabBarIcon: ({ color, size }) => (
            <Ionicons name="list-outline" color={color} size={size} />
          ),
        }}
      />
      <Tabs.Screen
        name="Lab"
        component={LabNavigator}
        options={{
          tabBarIcon: ({ color, size }) => (
            <Ionicons name="flask-outline" color={color} size={size} />
          ),
        }}
      />
    </Tabs.Navigator>
  )
}

function ModalRoute({
  navigation,
}: NativeStackScreenProps<RootStackParamList, 'Modal'>) {
  return <ModalScreen onClose={() => navigation.goBack()} />
}

function App() {
  const colorScheme = useColorScheme()
  const [loaded] = useFonts({
    SpaceMono: require('../assets/fonts/SpaceMono-Regular.ttf'),
  })

  useEffect(() => {
    // Once per JS session, not once per render.
    Observe.logEvent('app_started')
    Observe.dispatchEvents()
  }, [])

  useEffect(() => {
    if (loaded) {
      SplashScreen.hideAsync()
    }
  }, [loaded])

  if (!loaded) {
    return null
  }

  return (
    <ErrorBoundary>
      <ObserveNavigationContainer
        theme={colorScheme === 'dark' ? DarkTheme : DefaultTheme}
      >
        <RootStack.Navigator>
          <RootStack.Screen
            name="Tabs"
            component={TabsNavigator}
            options={{ headerShown: false }}
          />
          <RootStack.Screen
            name="Modal"
            component={ModalRoute}
            options={{ presentation: 'modal', title: 'Modal' }}
          />
        </RootStack.Navigator>
        <StatusBar style="auto" />
      </ObserveNavigationContainer>
    </ErrorBoundary>
  )
}

export const ReactNavigationApp = ObserveRoot.wrap(App)
