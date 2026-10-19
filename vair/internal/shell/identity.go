package shell

// AppID is the application identifier: the .desktop file basename, the icon
// name and the single-instance key.
const AppID = "com.vair.app"

// WMClassID is what the DESKTOP sees as this app's identity, and it is not
// AppID. Wails registers its GApplication as "org.wails." + a lowercased,
// sanitised Options.Name ("Vair" → org.wails.vair) and offers no way to
// override it; GTK4 then hands that same string to the compositor as the
// Wayland app_id, which is what GNOME matches windows on.
//
// Measured on GNOME 46: with StartupWMClass=com.vair.app the shell could not
// resolve the window to any installed entry and showed it in the dash as a
// generic gear labelled "org.wails.vair". So the desktop file declares THIS
// string in StartupWMClass, and ProgramName is set to it as well so the X11
// WM_CLASS agrees with the Wayland app_id. Keep the two in sync with
// build/linux/com.vair.app.desktop.
const WMClassID = "org.wails.vair"
