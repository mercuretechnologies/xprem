# Welcome to your Expo app 👋

This is an [Expo](https://expo.dev) project created with [`create-expo-app`](https://www.npmjs.com/package/create-expo-app).

## Telemetry and navigation

The app reports to `expo-observe`, which sends OTLP metrics and logs to the
`endpointUrl` declared under `extra.eas.observe` in `app.json`. Point that at
your own server to watch the payloads arrive.

`expo-observe` records per-screen metrics (`cold_ttr`, `warm_ttr` and `tti`)
only when a navigation integration is enabled, and it ships one integration per
framework. They cannot both run at once, because the React Navigation one needs
to own the navigation container that expo-router renders itself. So the app
carries two navigation trees over the same screens and you pick one when you
build the bundle:

```bash
yarn start                     # expo-router tree, routes live in app/
yarn start:react-navigation    # @react-navigation tree, defined in navigation/
```

The same prefix works on any other script, for instance
`yarn release_staging:react-navigation` to publish an update running the React
Navigation tree. `EXPO_PUBLIC_NAV` is inlined by Metro, so the choice is made
when the bundle is built and cannot change at runtime. `metro.config.js`
resolves the entry of the unused tree to nothing so that a bundle only ever
contains one of them, which is also what keeps Metro from rejecting a graph that
holds both expo-router and React Navigation. The React Navigation mode still
needs `EXPO_ROUTER_DISABLE_RN_NAVIGATION_CHECK=1`, which its scripts already
set, because expo-router stays resolvable through `expo-observe` itself.

Whichever tree is running, `Observe.configure()` happens in `observe.config.ts`
at module scope, before anything mounts. It also turns on `dispatchInDebug`,
without which a debug build would discard everything instead of sending it.

The Lab tab is where the interesting payloads come from. It opens a screen that
stays busy for two seconds so `tti` lands well above `ttr`, sends log events at
several severities, throws during render so the error boundary reports it, and
throws asynchronously so the global handler picks it up.

## Get started

1. Install dependencies

   ```bash
   npm install
   ```

2. Start the app

   ```bash
    npx expo start
   ```

In the output, you'll find options to open the app in a

- [development build](https://docs.expo.dev/develop/development-builds/introduction/)
- [Android emulator](https://docs.expo.dev/workflow/android-studio-emulator/)
- [iOS simulator](https://docs.expo.dev/workflow/ios-simulator/)
- [Expo Go](https://expo.dev/go), a limited sandbox for trying out app development with Expo

You can start developing by editing the files inside the **app** directory. This project uses [file-based routing](https://docs.expo.dev/router/introduction).

## Get a fresh project

When you're ready, run:

```bash
npm run reset-project
```

This command will move the starter code to the **app-example** directory and create a blank **app** directory where you can start developing.

## Learn more

To learn more about developing your project with Expo, look at the following resources:

- [Expo documentation](https://docs.expo.dev/): Learn fundamentals, or go into advanced topics with our [guides](https://docs.expo.dev/guides).
- [Learn Expo tutorial](https://docs.expo.dev/tutorial/introduction/): Follow a step-by-step tutorial where you'll create a project that runs on Android, iOS, and the web.

## Join the community

Join our community of developers creating universal apps.

- [Expo on GitHub](https://github.com/expo/expo): View our open source platform and contribute.
- [Discord community](https://chat.expo.dev): Chat with Expo users and ask questions.
