import { Link, Route, Routes } from 'react-router-dom'
import Search from './pages/Search.tsx'
import Title from './pages/Title.tsx'
import Watch from './pages/Watch.tsx'

export default function App() {
  return (
    <div className="app">
      <header className="app-header">
        <Link to="/" className="brand">
          streamr
        </Link>
      </header>
      <main className="app-main">
        <Routes>
          <Route path="/" element={<Search />} />
          <Route path="/title/:type/:id" element={<Title />} />
          <Route path="/watch" element={<Watch />} />
        </Routes>
      </main>
    </div>
  )
}
