import React from 'react'
import ReactDOM from 'react-dom/client'
import App from './App'
import { startRemoteEvents } from './remote'

ReactDOM.createRoot(document.getElementById('root') as HTMLElement).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
)

// In browser (phone) mode, receive backend events over SSE. No-op in the
// desktop WebView2, where events arrive through the native bridge. Called after
// render so the Wails runtime (which owns window._wails.dispatchWailsEvent) is
// initialised by App's imports.
startRemoteEvents()
