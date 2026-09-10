import React from 'react';
import Layout from '@theme/Layout';
import Link from '@docusaurus/Link';

const paths = [
  ['01', 'Start building', 'Create a project, define a model, generate a migration, and serve your first route.', '/docs/quickstart', 'Your first project'],
  ['02', 'Learn the core', 'Find models and queries, routing and APIs, forms, accounts, and application services.', '/docs/index', 'Explore the framework'],
  ['03', 'Add Admin', 'Build staff tools with model registration, lists, editable forms, relationships, and permissions.', '/docs/admin', 'Build your admin'],
  ['04', 'Run background work', 'Register tasks, run workers, compose workflows, and handle retries and scheduling.', '/docs/async', 'Use Async'],
];

export default function Home() {
  return (
    <Layout title="Documentation" description="Learn Gogo with a first-project tutorial, feature guides, and a complete Go API reference.">
      <main className="docs-home container">
        <div className="home-heading">
          <div className="home-kicker">GOGO DOCUMENTATION</div>
          <h1>Your backend.<br />A place for everything.</h1>
          <p>A structured Go framework with models, APIs, forms, and authentication. Add Admin and Async when you need them. Start small, then follow the feature you’re building.</p>
          <div className="home-actions">
            <Link className="button button--primary" to="/docs/quickstart">Build your first project →</Link>
            <Link className="button button--secondary" to="/docs/showcase">Run the Docker showcase</Link>
          </div>
          <p className="alpha-note"><span className="alpha-badge">1.0.0-alpha.1</span> An early release, not complete Django or Celery parity. <Link to="/docs/compatibility">See supported behavior and limits →</Link></p>
        </div>
        <section aria-labelledby="reading-paths">
          <div className="section-heading"><h2 id="reading-paths">Choose your next step</h2><Link to="/docs/index">Framework overview →</Link></div>
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
            <Link to="/docs/deployment">Deploy and run processes →</Link><Link to="/docs/troubleshooting">Troubleshooting →</Link>
          </div>
        </section>
      </main>
    </Layout>
  );
}
