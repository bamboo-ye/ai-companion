# iOS

`CompanionCore` is a Swift Package and can be tested without a full Xcode installation:

```bash
swift test --package-path mobile/ios
```

The application project is described in `project.yml`. Install Xcode 26+ and XcodeGen, then run:

```bash
cd mobile/ios
xcodegen generate
open AICompanion.xcodeproj
```

Set a development team before running on a device. Reminders access is requested only when the user enables system reminder synchronization.
