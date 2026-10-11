export enum BuildStep {
  GENERAL = 'Build',
  READ_BUILD_CONFIG = 'Read xprem.json',
  CHECK_ANDROID_TOOLS = 'Check local Android tools',
  CHECK_IOS_TOOLS = 'Check local iOS tools',
  SET_UP_BUILD_ENVIRONMENT = 'Set up build environment',
  FETCH_ANDROID_CREDENTIALS = 'Fetch Android signing credentials',
  PREPARE_IOS_CREDENTIALS = 'Prepare iOS signing credentials',
  PREPARE_PROJECT = 'Prepare project',
  READ_APP_CONFIG = 'Read app config',
  VALIDATE_ANDROID_BUNDLE = 'Validate Android bundle',
  VALIDATE_IOS_BUNDLE = 'Validate iOS bundle',
  ALLOCATE_BUILD_NUMBER = 'Allocate build number',
  CALCULATE_RUNTIME = 'Calculate Expo fingerprint and runtime',
  PREBUILD = 'Prebuild',
  POST_INSTALL_HOOK = 'Post-install hook',
  CONFIGURE_ANDROID_SIGNING = 'Configure Android signing',
  RESTORE_BUILD_CACHE = 'Restore build cache',
  SAVE_BUILD_CACHE = 'Save build cache',
  BUILD_APK = 'Building signed APK',
  BUILD_AAB = 'Building signed AAB',
  GRADLE_BUILD_PROFILE = 'Gradle build profile',
  CONFIGURE_XCODE_PROJECT = 'Configure Xcode project',
  INSTALL_PODS = 'Install pods',
  BUILD_IPA = 'Building signed IPA',
  PREPARE_ARTIFACTS = 'Prepare artifacts',
  UPLOAD_APPLICATION_ARCHIVE = 'Upload application archive',
}

export enum BuildStepResult {
  SUCCESS = 'success',
  FAIL = 'failed',
  WARNING = 'warning',
  SKIPPED = 'skipped',
}

export enum LogMarker {
  START_STEP = 'START_STEP',
  END_STEP = 'END_STEP',
}
