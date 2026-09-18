import React from 'react';
import OriginalHeading from '@theme-original/Heading';
import useBrokenLinks from '@docusaurus/useBrokenLinks';

// Single-source pages use their title as the original document's anchor.
export default function Heading({as, id, ...props}) {
  const {collectAnchor} = useBrokenLinks();
  if (as === 'h1' && id) {
    collectAnchor(id);
    return <h1 id={id} {...props} />;
  }
  return <OriginalHeading as={as} id={id} {...props} />;
}
