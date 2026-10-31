import React from 'react';
import { createRoot } from 'react-dom/client';
import './styles.css';

function App() {
  return (
    <main className="shell">
      <header>
        <p className="eyebrow">Prompt Driven Scraper</p>
        <h1>Deterministic scraping, profile driven.</h1>
        <p className="lede">AI can author profiles. The runtime stays deterministic.</p>
      </header>

      <section className="panel">
        <label>
          Query input
          <textarea defaultValue="I'm only looking for EV cars after 2024" />
        </label>
        <label>
          Search sources
          <textarea defaultValue={'Carsales\nCarsGuide\nDrive'} />
        </label>
        <label>
          Output
          <textarea defaultValue={'make:string\nmodel:string\nyear:integer\nprice:decimal\nkilometres:integer\nurl:url'} />
        </label>
        <button>Run</button>
      </section>

      <section className="panel progress">
        <div>
          <strong>Idle</strong>
          <span>0 records collected</span>
        </div>
        <progress value={0} max={100} />
        <div className="actions">
          <button disabled>Pause</button>
          <button disabled>Cancel</button>
          <button disabled>Give me what you've got</button>
        </div>
      </section>
    </main>
  );
}

createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
