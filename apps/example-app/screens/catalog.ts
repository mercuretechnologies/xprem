export type CatalogItem = {
  id: string
  title: string
  subtitle: string
}

/**
 * Static list. The point is not the data, it is that opening an item produces a
 * navigation metric carrying route params, so the server sees a route pattern
 * (`/catalog/[id]`) distinct from the concrete path.
 */
export const CATALOG_ITEMS: CatalogItem[] = [
  { id: '1', title: 'Manifest', subtitle: 'What the client polls' },
  { id: '2', title: 'Assets', subtitle: 'Hashed bundle files' },
  { id: '3', title: 'Branches', subtitle: 'Where updates live' },
  { id: '4', title: 'Channels', subtitle: 'What builds point at' },
  { id: '5', title: 'Rollouts', subtitle: 'Partial delivery' },
  { id: '6', title: 'Rollbacks', subtitle: 'Back to embedded' },
  { id: '7', title: 'Code signing', subtitle: 'Signed manifests' },
  { id: '8', title: 'Telemetry', subtitle: 'Metrics and logs' },
]

export function findCatalogItem(id: string): CatalogItem | undefined {
  return CATALOG_ITEMS.find((item) => item.id === id)
}
