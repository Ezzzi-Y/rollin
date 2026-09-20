import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router'

import './index.css'
import App from './App.tsx'
import { installUnauthorizedRedirect } from './auth/unauthorizedRedirect'

// 401 全局处理：会话失效时跳对应登录页（平台 / 活动）
installUnauthorizedRedirect()

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <BrowserRouter>
      <App />
    </BrowserRouter>
  </StrictMode>,
)
