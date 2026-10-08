import { Fragment } from "react";

export default function DomainName({ name }: { name: string }) {
  const labels = name.split(".");

  return (
    <>
      {labels.map((label, i) => (
        <Fragment key={i}>
          {label}
          {i < labels.length - 1 && (
            <>
              .<wbr />
            </>
          )}
        </Fragment>
      ))}
    </>
  );
}
