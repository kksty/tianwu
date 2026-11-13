import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { ThemeProvider } from '@cyber/theme'
import { ConfirmProvider } from '@cyber/ui'
import App from './App'
import './app.css'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <ThemeProvider className="aspect-theme-root h-full">
      <ConfirmProvider labels={{ title: '确认', confirm: '确定', cancel: '取消', close: '关闭' }}>
        <App />
      </ConfirmProvider>
    </ThemeProvider>
  </StrictMode>,
)
