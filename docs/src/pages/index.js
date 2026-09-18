import React from 'react';
import {Redirect} from '@docusaurus/router';
import Link from '@docusaurus/Link';

export default function Home() {
  return <><Redirect to="/docs/index/" /><main><Link to="/docs/index/">Gogo documentation</Link></main></>;
}
