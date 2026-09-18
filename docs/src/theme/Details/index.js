import React from 'react';

// Native disclosure state also works for keyboard users, no-JS readers and
// direct links. No animated wrapper can keep a linked declaration hidden.
export default function Details({summary, children, ...props}) {
  const heading = React.isValidElement(summary)
    ? summary
    : <summary>{summary ?? 'Details'}</summary>;
  return <details {...props}>{heading}{children}</details>;
}
