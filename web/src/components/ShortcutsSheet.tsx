import { Modal } from './ui'

const GROUPS: { title: string; keys: [string[], string][] }[] = [
  { title: 'Go to', keys: [[['i'], 'Open issues'], [['r'], 'Resolved issues'], [['g'], 'Ignored issues'], [['p'], 'Projects'], [[','], 'Settings']] },
  { title: 'On an issue', keys: [[['e'], 'Resolve'], [['o'], 'Open again'], [['j'], 'An older event'], [['k'], 'A newer event']] },
  { title: 'In the list', keys: [[['j'], 'Next issue'], [['k'], 'Previous issue'], [['enter'], 'Open it'], [['x'], 'Select it'], [['e'], 'Resolve the selected']] },
  { title: 'Add', keys: [[['n'], 'New project']] },
  { title: 'Find', keys: [[['/'], 'Search & commands'], [['⌘', 'K'], 'Search & commands']] },
  { title: 'Window', keys: [[['['], 'Show or hide the sidebar'], [['esc'], 'Close a dialog or menu']] },
  { title: 'Help', keys: [[['?'], 'This sheet']] },
]

export function ShortcutsSheet({ onClose }: { onClose: () => void }) {
  return (
    <Modal title="Keyboard shortcuts" onClose={onClose} size="lg">
      <div className="shortcuts">
        {GROUPS.map(g => (
          <section key={g.title}>
            <h4 className="sc-h">{g.title}</h4>
            {g.keys.map(([keys, label]) => (
              <div key={label + keys.join('')} className="sc-row"><span>{label}</span><span className="sc-keys">{keys.map(k => <kbd key={k}>{k}</kbd>)}</span></div>
            ))}
          </section>
        ))}
      </div>
    </Modal>
  )
}
