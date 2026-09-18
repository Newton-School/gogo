import React from 'react';
import Layout from '@theme/Layout';
import Link from '@docusaurus/Link';

const paths = [
  ['01', 'Your first backend', 'Install → create a project → run it → persist products → expose an API → package with Docker.', '/docs/quickstart', 'Follow the tutorial'],
  ['02', 'Find a feature', 'Browse models, fields, forms, APIs, accounts, and services by what you want to build.', '/docs/features', 'Explore all features'],
  ['03', 'Build staff tools', 'Wire authentication and sessions, register models, then customize lists, forms, and actions.', '/docs/admin-wiring', 'Set up Admin'],
  ['04', 'Run background work', 'Connect Redis, register typed tasks, start a worker, and try workflow and schedule recipes.', '/docs/async-wiring', 'Wire a queue'],
];

export default function Home() {
  return (
    <Layout title="Documentation" description="Learn Gogo with a first-project tutorial, feature guides, and a complete Go API reference.">
      <main className="docs-home container">
        <div className="home-heading">
          <div className="home-kicker">GOGO DOCUMENTATION</div>
          <h1>From your first route<br />to a working backend.</h1>
          <p>Build with Gogo, step by step. Create a Go project, store real data, expose an API, and run it in Docker. Then add authenticated Admin pages and background workers.</p>
          <div className="home-actions">
            <Link className="button button--primary" to="/docs/quickstart">Build your first project →</Link>
            <Link className="button button--secondary" to="/docs/showcase">Run the Docker showcase</Link>
          </div>
          <p className="alpha-note"><span className="alpha-badge">1.0.0-alpha.1</span> An early release, not complete Django or Celery parity. <Link to="/docs/compatibility">See supported behavior and limits →</Link></p>
        </div>
        <section aria-labelledby="reading-paths">
          <div className="section-heading"><h2 id="reading-paths">A clear path from setup to shipping</h2><Link to="/docs/installation">Install and prepare →</Link></div>
          <div className="path-grid">
            {paths.map(([number, title, description, to, action]) => (
              <Link className="path-card" to={to} key={number}>
                <span className="path-number" aria-hidden="true">{number}</span>
                <h3>{title}</h3><p>{description}</p><span className="path-action">{action} →</span>
              </Link>
            ))}
          </div>
        </section>
        <section className="lookup-section" aria-labelledby="lookup-title">
          <div><h2 id="lookup-title">Already know what you need?</h2><p>Jump straight to the reference. Search also finds Go symbols.</p></div>
          <div className="lookup-links">
            <Link to="/docs/model-fields">Model fields →</Link><Link to="/docs/forms">Forms and widgets →</Link>
            <Link to="/docs/settings">Environment settings →</Link><Link to="/docs/packages">Go API reference →</Link>
            <Link to="/docs/running">Run and build →</Link><Link to="/docs/docker">Docker setup →</Link>
            <Link to="/docs/deployment">Deploy and run processes →</Link><Link to="/docs/troubleshooting">Troubleshooting →</Link>
          </div>
        </section>
      </main>
    </Layout>
  );
}
