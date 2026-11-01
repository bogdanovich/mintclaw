import unittest
from native_text_plan import plan_complete_units


class CompleteUnitPlanTests(unittest.TestCase):
    def test_order_exact_boundary_and_expected_partial_effect(self):
        units = [([[10]] * 30, "a"), ([[11], [12]], "aб"), ([[13]], "aб界")]
        actions = plan_complete_units(units)
        self.assertEqual(tuple(len(chords) for chords, _ in actions), (32, 1))
        self.assertEqual(tuple(state for _, state in actions), ("aб", "aб界"))
        self.assertEqual([chord for chords, _ in actions for chord in chords],
                         [tuple(chord) for unit, _ in units for chord in unit])

    def test_unicode_compositions_are_never_split_and_input_is_not_modified(self):
        units = []
        text = ""
        for character in "Привет 世界 café 👩‍💻 e\u0301 مرحبا":
            composition = [[37, 50, 30]] + [[ord(digit)] for digit in format(ord(character), "x")] + [[36]]
            text += character
            units.append((composition, text))
        before = [[list(chord) for chord in unit] for unit, _ in units]
        actions = plan_complete_units(units)
        cursor = 0
        boundaries = set()
        for chords, _ in actions:
            self.assertLessEqual(len(chords), 32)
            cursor += len(chords)
            boundaries.add(cursor)
        ends, cursor = set(), 0
        for unit, _ in units:
            cursor += len(unit)
            ends.add(cursor)
        self.assertTrue(boundaries.issubset(ends))
        self.assertEqual(actions[-1][1], text)
        self.assertEqual(before, [unit for unit, _ in units])
        saved = actions[0][0][0]
        units[0][0][0].append(99)
        self.assertEqual(actions[0][0][0], saved)

    def test_total_bound_empty_and_oversized_complete_units(self):
        self.assertEqual(len(plan_complete_units([([[10]] * 32, str(i)) for i in range(8)])), 8)
        for units in [[], [([], "")], [([[10]] * 33, "x")],
                      [([[10]] * 32, str(i)) for i in range(8)] + [([[11]], "extra")]]:
            with self.assertRaises(ValueError):
                plan_complete_units(units)


if __name__ == "__main__":
    unittest.main()
